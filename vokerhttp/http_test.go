package vokerhttp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsTextContent(t *testing.T) {
	tests := []struct {
		contentType string
		want        bool
	}{
		{"text/html", true},
		{"text/plain", true},
		{"text/css", true},
		{"text/csv", true},
		{"text/html; charset=utf-8", true},
		{"application/json", true},
		{"application/json; charset=utf-8", true},
		{"application/xml", true},
		{"application/javascript", true},
		{"application/x-www-form-urlencoded", true},
		{"application/vnd.api+json", true},
		{"application/atom+xml", true},
		{"application/octet-stream", false},
		{"image/png", false},
		{"image/jpeg", false},
		{"application/protobuf", false},
		{"application/gzip", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.contentType, func(t *testing.T) {
			assert.Equal(t, tt.want, isTextContent(tt.contentType))
		})
	}
}

// errAdapter is an Adapter whose Request always fails, used to exercise the
// request error path in eventHandler.
type errAdapter struct{}

func (errAdapter) Request(ctx context.Context, event struct{}) (*http.Request, error) {
	return nil, io.ErrUnexpectedEOF
}

func (errAdapter) Response(resp *http.Response) (struct{}, error) { return struct{}{}, nil }

// respErrAdapter is an Adapter whose Response always fails, used to exercise
// the response error path in eventHandler.
type respErrAdapter struct{}

func (respErrAdapter) Request(ctx context.Context, event struct{}) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, "GET", "https://example.com/", nil)
}

func (respErrAdapter) Response(resp *http.Response) (struct{}, error) {
	return struct{}{}, io.ErrClosedPipe
}

func TestEventHandler_EndToEnd(t *testing.T) {
	var gotEvent FunctionURLRequest
	var sawEvent bool

	mux := http.NewServeMux()
	mux.HandleFunc("/my/path", func(w http.ResponseWriter, r *http.Request) {
		// The original Lambda event must be retrievable from the context.
		gotEvent, sawEvent = EventFromContext[FunctionURLRequest](r.Context())

		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/my/path", r.URL.Path)
		assert.Equal(t, "bar", r.URL.Query().Get("foo"))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	handler := eventHandler(mux, &FunctionURL{})
	resp, err := handler(context.Background(), newTestFunctionURLRequest())
	require.NoError(t, err)

	require.True(t, sawEvent)
	assert.Equal(t, "/my/path", gotEvent.RawPath)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, `{"ok":true}`, resp.Body)
	assert.False(t, resp.IsBase64Encoded)
}

func TestEventHandler_RequestError(t *testing.T) {
	handler := eventHandler(http.NewServeMux(), errAdapter{})

	_, err := handler(context.Background(), struct{}{})
	require.Error(t, err)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.Contains(t, err.Error(), "failed to build http request")
}

func TestEventHandler_ResponseError(t *testing.T) {
	handler := eventHandler(http.NewServeMux(), respErrAdapter{})

	_, err := handler(context.Background(), struct{}{})
	require.Error(t, err)
	assert.ErrorIs(t, err, io.ErrClosedPipe)
	assert.Contains(t, err.Error(), "failed to build lambda response")
}

// TestAdapterResponse_StreamedBody verifies adapters accept any *http.Response,
// not just recorder output: the body may come from an arbitrary stream.
func TestAdapterResponse_StreamedBody(t *testing.T) {
	adapter := &FunctionURL{}
	resp, err := adapter.Response(&http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(strings.NewReader("streamed body")),
	})
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "streamed body", resp.Body)
	assert.False(t, resp.IsBase64Encoded)
}

// failingReader errors mid-stream to exercise the body read error path.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestAdapterResponse_BodyReadError(t *testing.T) {
	adapter := &FunctionURL{}
	_, err := adapter.Response(&http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(failingReader{}),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.Contains(t, err.Error(), "failed to read response body")
}

func TestEventFromContext(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		event := newTestFunctionURLRequest()
		ctx := context.WithValue(context.Background(), eventContextKey{}, event)

		got, ok := EventFromContext[FunctionURLRequest](ctx)
		require.True(t, ok)
		assert.Equal(t, "/my/path", got.RawPath)
	})

	t.Run("missing", func(t *testing.T) {
		_, ok := EventFromContext[FunctionURLRequest](context.Background())
		assert.False(t, ok)
	})

	t.Run("wrong type", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), eventContextKey{}, newTestFunctionURLRequest())

		// Requesting a different event type than what was stored returns false.
		_, ok := EventFromContext[ALBRequest](ctx)
		assert.False(t, ok)
	})

	t.Run("distinct v2 event types", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), eventContextKey{}, newTestFunctionURLRequest())

		_, ok := EventFromContext[APIGatewayV2Request](ctx)
		assert.False(t, ok)
	})
}
