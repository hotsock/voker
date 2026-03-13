package vokerhttp

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hotsock/voker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestAPIGatewayV1Request() APIGatewayV1Request {
	return APIGatewayV1Request{
		Resource:   "/my/path",
		Path:       "/my/path",
		HTTPMethod: "GET",
		Headers: map[string]string{
			"Host":         "abc123.execute-api.us-east-1.amazonaws.com",
			"Content-Type": "application/json",
			"X-Custom":     "value",
		},
		MultiValueHeaders: map[string][]string{
			"Host":         {"abc123.execute-api.us-east-1.amazonaws.com"},
			"Content-Type": {"application/json"},
			"X-Custom":     {"value"},
		},
		QueryStringParameters: map[string]string{
			"foo": "bar",
		},
		MultiValueQueryStringParameters: map[string][]string{
			"foo": {"bar"},
		},
		RequestContext: APIGatewayV1RequestContext{
			AccountID:  "123456789012",
			APIID:      "abc123",
			DomainName: "abc123.execute-api.us-east-1.amazonaws.com",
			HTTPMethod: "GET",
			Identity: APIGatewayV1RequestIdentity{
				SourceIP:  "1.2.3.4",
				UserAgent: "TestAgent/1.0",
			},
			Path:      "/my/path",
			Protocol:  "HTTP/1.1",
			RequestID: "req-123",
			Stage:     "$default",
		},
	}
}

func TestAPIGatewayV1Request_Basic(t *testing.T) {
	adapter := &APIGatewayV1{}
	event := newTestAPIGatewayV1Request()

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, "/my/path", req.URL.Path)
	assert.Equal(t, "bar", req.URL.Query().Get("foo"))
	assert.Equal(t, "abc123.execute-api.us-east-1.amazonaws.com", req.URL.Host)
	assert.Equal(t, "https", req.URL.Scheme)
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Equal(t, "value", req.Header.Get("X-Custom"))
	assert.Equal(t, "1.2.3.4", req.RemoteAddr)
}

func TestAPIGatewayV1Request_WithBody(t *testing.T) {
	adapter := &APIGatewayV1{}
	event := newTestAPIGatewayV1Request()
	event.HTTPMethod = "POST"
	event.Body = `{"key":"value"}`

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, `{"key":"value"}`, string(body))
}

func TestAPIGatewayV1Request_Base64Body(t *testing.T) {
	adapter := &APIGatewayV1{}
	event := newTestAPIGatewayV1Request()
	event.HTTPMethod = "POST"
	event.Body = base64.StdEncoding.EncodeToString([]byte("binary data"))
	event.IsBase64Encoded = true

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, "binary data", string(body))
}

func TestAPIGatewayV1Request_MultiValueQueryParams(t *testing.T) {
	adapter := &APIGatewayV1{}
	event := newTestAPIGatewayV1Request()
	event.MultiValueQueryStringParameters = map[string][]string{
		"color": {"red", "blue"},
		"size":  {"large"},
	}

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"red", "blue"}, req.URL.Query()["color"])
	assert.Equal(t, "large", req.URL.Query().Get("size"))
}

func TestAPIGatewayV1Request_MultiValueHeaders(t *testing.T) {
	adapter := &APIGatewayV1{}
	event := newTestAPIGatewayV1Request()
	event.MultiValueHeaders = map[string][]string{
		"Accept":       {"text/html", "application/json"},
		"Content-Type": {"application/json"},
	}

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, []string{"text/html", "application/json"}, req.Header.Values("Accept"))
}

func TestAPIGatewayV1Request_FallsBackToSingleValueHeaders(t *testing.T) {
	adapter := &APIGatewayV1{}
	event := newTestAPIGatewayV1Request()
	event.MultiValueHeaders = nil

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
}

func TestAPIGatewayV1Request_NoQueryString(t *testing.T) {
	adapter := &APIGatewayV1{}
	event := newTestAPIGatewayV1Request()
	event.QueryStringParameters = nil
	event.MultiValueQueryStringParameters = nil

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "", req.URL.RawQuery)
	assert.Equal(t, "/my/path", req.RequestURI)
}

func TestAPIGatewayV1Request_ContextPropagation(t *testing.T) {
	adapter := &APIGatewayV1{}
	event := newTestAPIGatewayV1Request()

	lc := &voker.LambdaContext{
		AwsRequestID:       "req-456",
		InvokedFunctionArn: "arn:aws:lambda:us-east-1:123:function:test",
	}
	ctx := voker.NewContext(context.Background(), lc)

	req, err := adapter.Request(ctx, event)
	require.NoError(t, err)

	gotLC, ok := voker.FromContext(req.Context())
	require.True(t, ok)
	assert.Equal(t, "req-456", gotLC.AwsRequestID)
}

func TestAPIGatewayV1Response_TextBody(t *testing.T) {
	adapter := &APIGatewayV1{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/html")
	recorder.WriteHeader(http.StatusOK)
	recorder.Write([]byte("<h1>Hello</h1>"))

	resp := adapter.Response(recorder)

	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "<h1>Hello</h1>", resp.Body)
	assert.False(t, resp.IsBase64Encoded)
}

func TestAPIGatewayV1Response_BinaryBody(t *testing.T) {
	adapter := &APIGatewayV1{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "image/png")
	recorder.WriteHeader(http.StatusOK)
	data := []byte{0x89, 0x50, 0x4E, 0x47}
	recorder.Write(data)

	resp := adapter.Response(recorder)

	assert.True(t, resp.IsBase64Encoded)
	decoded, err := base64.StdEncoding.DecodeString(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, data, decoded)
}

func TestAPIGatewayV1Response_MultiValueHeaders(t *testing.T) {
	adapter := &APIGatewayV1{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/plain")
	recorder.Header().Add("Set-Cookie", "session=abc; HttpOnly")
	recorder.Header().Add("Set-Cookie", "theme=dark; Path=/")
	recorder.Header().Add("X-Custom", "val1")
	recorder.Header().Add("X-Custom", "val2")
	recorder.WriteHeader(http.StatusOK)
	recorder.Write([]byte("ok"))

	resp := adapter.Response(recorder)

	// All headers go into MultiValueHeaders
	assert.Contains(t, resp.MultiValueHeaders["set-cookie"], "session=abc; HttpOnly")
	assert.Contains(t, resp.MultiValueHeaders["set-cookie"], "theme=dark; Path=/")
	assert.Equal(t, []string{"val1", "val2"}, resp.MultiValueHeaders["x-custom"])
	// Single-value Headers should be nil (we use MultiValueHeaders exclusively)
	assert.Nil(t, resp.Headers)
}
