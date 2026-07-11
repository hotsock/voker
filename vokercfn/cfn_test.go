package vokercfn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type properties struct {
	Name string `json:"Name"`
}

type data struct {
	ARN string `json:"Arn"`
}

type recordingClient struct {
	calls    int
	req      *http.Request
	body     []byte
	response *http.Response
	err      error
}

func (c *recordingClient) Do(req *http.Request) (*http.Response, error) {
	c.calls++
	c.req = req
	c.body, _ = io.ReadAll(req.Body)
	if c.err != nil {
		return nil, c.err
	}
	if c.response != nil {
		return c.response, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: http.NoBody}, nil
}

type flakyClient struct {
	calls int
}

func (c *flakyClient) Do(*http.Request) (*http.Response, error) {
	c.calls++
	if c.calls == 1 {
		return nil, io.ErrUnexpectedEOF
	}
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: http.NoBody}, nil
}

func createEvent() Event[properties] {
	return Event[properties]{
		RequestType:       RequestCreate,
		RequestID:         "request-123",
		ResponseURL:       "https://example.com/presigned",
		ResourceType:      "Custom::Thing",
		LogicalResourceID: "Thing",
		StackID:           "stack-123",
		ResourceProperties: properties{
			Name: "example",
		},
	}
}

func decodeResponse(t *testing.T, body []byte) response {
	t.Helper()
	var got response
	require.NoError(t, json.Unmarshal(body, &got))
	return got
}

func TestWrapSuccess(t *testing.T) {
	client := &recordingClient{}
	handler := func(_ context.Context, event Event[properties]) (Result[data], error) {
		assert.Equal(t, "example", event.ResourceProperties.Name)
		return Result[data]{
			PhysicalResourceID: "thing-123",
			Data:               data{ARN: "arn:example"},
			NoEcho:             true,
		}, nil
	}

	_, err := wrapWithClient(Handler[properties, data](handler), client)(context.Background(), createEvent())
	require.NoError(t, err)
	require.NotNil(t, client.req)
	assert.Equal(t, http.MethodPut, client.req.Method)
	assert.NotContains(t, client.req.Header, "Content-Type")

	got := decodeResponse(t, client.body)
	assert.Equal(t, statusSuccess, got.Status)
	assert.Equal(t, "thing-123", got.PhysicalResourceID)
	assert.Equal(t, "request-123", got.RequestID)
	assert.Equal(t, "Thing", got.LogicalResourceID)
	assert.Equal(t, "stack-123", got.StackID)
	assert.True(t, got.NoEcho)
	assert.Equal(t, map[string]any{"Arn": "arn:example"}, got.Data)
}

func TestWrapHandlerErrorSendsFailureAndReturnsSuccess(t *testing.T) {
	client := &recordingClient{}
	wantErr := errors.New("provisioning failed")
	handler := func(context.Context, Event[properties]) (Result[data], error) {
		return Result[data]{PhysicalResourceID: "partial-123", Data: data{ARN: "secret"}, NoEcho: true}, wantErr
	}

	_, err := wrapWithClient(Handler[properties, data](handler), client)(context.Background(), createEvent())
	require.NoError(t, err)

	got := decodeResponse(t, client.body)
	assert.Equal(t, statusFailed, got.Status)
	assert.Equal(t, wantErr.Error(), got.Reason)
	assert.Equal(t, "partial-123", got.PhysicalResourceID)
	assert.Nil(t, got.Data)
	assert.False(t, got.NoEcho)
}

func TestWrapEmptyHandlerErrorStillIncludesRequiredReason(t *testing.T) {
	client := &recordingClient{}
	handler := func(context.Context, Event[properties]) (Result[data], error) {
		return Result[data]{}, errors.New("")
	}

	_, err := wrapWithClient(Handler[properties, data](handler), client)(context.Background(), createEvent())
	require.NoError(t, err)
	assert.Equal(t, "handler returned an error", decodeResponse(t, client.body).Reason)
}

func TestWrapIncludesZeroValueData(t *testing.T) {
	client := &recordingClient{}
	handler := func(context.Context, Event[properties]) (Result[data], error) {
		return Result[data]{}, nil
	}

	_, err := wrapWithClient(Handler[properties, data](handler), client)(context.Background(), createEvent())
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"Arn": ""}, decodeResponse(t, client.body).Data)
}

func TestWrapPhysicalResourceIDFallbacks(t *testing.T) {
	tests := []struct {
		name  string
		event Event[properties]
		want  string
	}{
		{name: "create", event: createEvent(), want: "request-123"},
		{name: "update", event: func() Event[properties] {
			e := createEvent()
			e.RequestType = RequestUpdate
			e.PhysicalResourceID = "existing-123"
			return e
		}(), want: "existing-123"},
		{name: "delete", event: func() Event[properties] {
			e := createEvent()
			e.RequestType = RequestDelete
			e.PhysicalResourceID = "existing-123"
			return e
		}(), want: "existing-123"},
		{name: "missing update id", event: func() Event[properties] {
			e := createEvent()
			e.RequestType = RequestUpdate
			return e
		}(), want: "request-123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &recordingClient{}
			handler := func(context.Context, Event[properties]) (Result[data], error) {
				return Result[data]{}, nil
			}
			_, err := wrapWithClient(Handler[properties, data](handler), client)(context.Background(), tt.event)
			require.NoError(t, err)
			assert.Equal(t, tt.want, decodeResponse(t, client.body).PhysicalResourceID)
		})
	}
}

func TestWrapPanicSendsFailureAndRepanics(t *testing.T) {
	client := &recordingClient{}
	handler := func(context.Context, Event[properties]) (Result[data], error) {
		panic("boom")
	}

	assert.PanicsWithValue(t, "boom", func() {
		_, _ = wrapWithClient(Handler[properties, data](handler), client)(context.Background(), createEvent())
	})
	got := decodeResponse(t, client.body)
	assert.Equal(t, statusFailed, got.Status)
	assert.Equal(t, "handler panicked; see Lambda logs for details", got.Reason)
	assert.Equal(t, "request-123", got.PhysicalResourceID)
}

func TestWrapResponseDeliveryError(t *testing.T) {
	client := &recordingClient{err: errors.New("connection refused")}
	handler := func(context.Context, Event[properties]) (Result[data], error) {
		return Result[data]{}, nil
	}

	_, err := wrapWithClient(Handler[properties, data](handler), client)(context.Background(), createEvent())
	require.Error(t, err)
	assert.ErrorContains(t, err, "send CloudFormation response")
	assert.ErrorContains(t, err, "connection refused")
}

func TestWrapTypedDecodeErrorSendsCloudFormationFailure(t *testing.T) {
	type incompatibleProperties struct {
		Count int `json:"Count"`
	}

	payload, err := os.ReadFile("testdata/create-request.json")
	require.NoError(t, err)
	client := &recordingClient{}
	handlerCalled := false
	handler := func(context.Context, Event[incompatibleProperties]) (Result[data], error) {
		handlerCalled = true
		return Result[data]{}, nil
	}

	_, err = wrapRawWithClient(Handler[incompatibleProperties, data](handler), client)(context.Background(), payload)
	require.NoError(t, err)
	assert.False(t, handlerCalled)

	got := decodeResponse(t, client.body)
	assert.Equal(t, statusFailed, got.Status)
	assert.Equal(t, "6245fb70-1335-46ef-8452-2a0cde2f8238", got.PhysicalResourceID)
	assert.Contains(t, got.Reason, "cannot unmarshal string into Go struct field")
}

func TestWrapMalformedEventWithoutMetadataReturnsInvocationError(t *testing.T) {
	client := &recordingClient{}
	handler := func(context.Context, Event[properties]) (Result[data], error) {
		return Result[data]{}, nil
	}

	_, err := wrapRawWithClient(Handler[properties, data](handler), client)(context.Background(), json.RawMessage(`not json`))
	require.Error(t, err)
	assert.ErrorContains(t, err, "decode CloudFormation event metadata")
	assert.Nil(t, client.req)
}

func TestSendResponseRejectsNonOKStatus(t *testing.T) {
	client := &recordingClient{response: &http.Response{
		StatusCode: http.StatusForbidden,
		Status:     "403 Forbidden",
		Body:       io.NopCloser(strings.NewReader("access denied")),
	}}

	err := sendResponse(context.Background(), "https://example.com", response{Status: statusSuccess}, client)
	require.Error(t, err)
	assert.ErrorContains(t, err, "403 Forbidden")
	assert.ErrorContains(t, err, "access denied")
	assert.Equal(t, 1, client.calls)
}

func TestSendResponseRetriesTransientNetworkError(t *testing.T) {
	client := &flakyClient{}
	err := sendResponse(context.Background(), "https://example.com", response{Status: statusSuccess}, client)
	require.NoError(t, err)
	assert.Equal(t, 2, client.calls)
}

func TestMarshalResponseReplacesInvalidOrOversizedDataWithFailure(t *testing.T) {
	tests := []struct {
		name string
		data any
	}{
		{name: "invalid", data: make(chan int)},
		{name: "oversized", data: strings.Repeat("x", maxResponseBytes)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := marshalResponse(response{
				Status:             statusSuccess,
				PhysicalResourceID: "physical-123",
				StackID:            "stack-123",
				RequestID:          "request-123",
				LogicalResourceID:  "Thing",
				Data:               tt.data,
			})
			assert.LessOrEqual(t, len(body), maxResponseBytes)
			got := decodeResponse(t, body)
			assert.Equal(t, statusFailed, got.Status)
			assert.NotEmpty(t, got.Reason)
			assert.Nil(t, got.Data)
		})
	}
}

func TestMarshalResponseTruncatesLongFailureReason(t *testing.T) {
	body := marshalResponse(response{
		Status:             statusFailed,
		Reason:             strings.Repeat("🙂", maxResponseBytes),
		PhysicalResourceID: "physical-123",
		StackID:            "stack-123",
		RequestID:          "request-123",
		LogicalResourceID:  "Thing",
	})
	assert.LessOrEqual(t, len(body), maxResponseBytes)
	assert.True(t, json.Valid(body))
	assert.True(t, utf8Valid(body))
}

type observedProperties struct {
	ServiceToken string `json:"ServiceToken"`
	Message      string `json:"Message"`
	Count        string `json:"Count"`
}

func TestObservedCloudFormationLifecycle(t *testing.T) {
	for _, operation := range []string{"create", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			requestJSON, err := os.ReadFile("testdata/" + operation + "-request.json")
			require.NoError(t, err)
			var event Event[observedProperties]
			require.NoError(t, json.Unmarshal(requestJSON, &event))

			expectedJSON, err := os.ReadFile("testdata/" + operation + "-response.json")
			require.NoError(t, err)
			var expected response
			require.NoError(t, json.Unmarshal(expectedJSON, &expected))

			client := &recordingClient{}
			handler := func(_ context.Context, event Event[observedProperties]) (Result[map[string]string], error) {
				physicalID := event.PhysicalResourceID
				if physicalID == "" {
					physicalID = "voker-cloudformation-observed-resource"
				}
				return Result[map[string]string]{
					PhysicalResourceID: physicalID,
					Data: map[string]string{
						"Echo":        event.ResourceProperties.Message,
						"Count":       event.ResourceProperties.Count,
						"RequestType": string(event.RequestType),
					},
				}, nil
			}

			_, err = wrapWithClient(Handler[observedProperties, map[string]string](handler), client)(context.Background(), event)
			require.NoError(t, err)
			assert.JSONEq(t, string(expectedJSON), string(client.body))

			actual := decodeResponse(t, client.body)
			assert.Equal(t, expected.Status, actual.Status)
			assert.Equal(t, expected.PhysicalResourceID, actual.PhysicalResourceID)
			if event.RequestType == RequestUpdate {
				assert.Equal(t, "7", event.OldResourceProperties.Count)
				assert.Equal(t, "hello from CloudFormation", event.OldResourceProperties.Message)
			}
		})
	}
}

func utf8Valid(b []byte) bool {
	return bytes.Equal(bytes.ToValidUTF8(b, nil), b)
}
