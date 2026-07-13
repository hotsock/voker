package vokerhttp

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
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

func TestIsEncodedContent(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   bool
	}{
		{"absent", nil, false},
		{"gzip", []string{"gzip"}, true},
		{"br", []string{"br"}, true},
		{"zstd", []string{"zstd"}, true},
		{"uppercase", []string{"GZIP"}, true},
		{"identity", []string{"identity"}, false},
		{"identity with spaces", []string{" identity "}, false},
		{"multiple codings", []string{"gzip, br"}, true},
		{"identity then gzip", []string{"identity, gzip"}, true},
		{"multiple header lines", []string{"identity", "gzip"}, true},
		{"empty value", []string{""}, false},
		{"empty tokens", []string{", ,"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			for _, v := range tt.values {
				header.Add("Content-Encoding", v)
			}
			assert.Equal(t, tt.want, isEncodedContent(header))
		})
	}
}

// TestResponseBody_ContentEncoding verifies that a non-identity
// Content-Encoding forces base64 regardless of Content-Type: compressed
// bytes are not the media type the Content-Type describes, and returning
// them as a plain JSON string corrupts them.
func TestResponseBody_ContentEncoding(t *testing.T) {
	gzipBody := gzipCompress(t, `{"ok":true}`)

	t.Run("gzip with text content type is base64", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":     []string{"application/json"},
				"Content-Encoding": []string{"gzip"},
			},
			Body: io.NopCloser(bytes.NewReader(gzipBody)),
		}

		body, isBase64, err := responseBody(resp)
		require.NoError(t, err)
		assert.True(t, isBase64)

		decoded, err := base64.StdEncoding.DecodeString(body)
		require.NoError(t, err)
		assert.Equal(t, gzipBody, decoded)
	})

	t.Run("identity with text content type stays plain", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":     []string{"application/json"},
				"Content-Encoding": []string{"identity"},
			},
			Body: io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}

		body, isBase64, err := responseBody(resp)
		require.NoError(t, err)
		assert.False(t, isBase64)
		assert.Equal(t, `{"ok":true}`, body)
	})

	t.Run("content encoding disables sniffing", func(t *testing.T) {
		// Match net/http servers: an explicit Content-Encoding means the body
		// bytes don't reflect the media type, so no Content-Type is sniffed.
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Encoding": []string{"gzip"}},
			Body:       io.NopCloser(bytes.NewReader(gzipBody)),
		}

		_, isBase64, err := responseBody(resp)
		require.NoError(t, err)
		assert.True(t, isBase64)
		assert.Empty(t, resp.Header.Get("Content-Type"))
	})
}

// TestEventHandler_GzipResponse is the end-to-end regression test for the
// compression-middleware case: a handler that gzips its output while keeping
// the original Content-Type must produce a base64 Lambda response.
func TestEventHandler_GzipResponse(t *testing.T) {
	const payload = `{"ok":true}`

	mux := http.NewServeMux()
	mux.HandleFunc("/my/path", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		_, _ = gz.Write([]byte(payload))
		require.NoError(t, gz.Close())
	})

	handler := eventHandler(mux, &FunctionURL{})
	resp, err := handler(context.Background(), newTestFunctionURLRequest())
	require.NoError(t, err)

	require.True(t, resp.IsBase64Encoded)
	assert.Equal(t, "application/json", resp.Headers["content-type"])
	assert.Equal(t, "gzip", resp.Headers["content-encoding"])

	compressed, err := base64.StdEncoding.DecodeString(resp.Body)
	require.NoError(t, err)
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	require.NoError(t, err)
	decompressed, err := io.ReadAll(gz)
	require.NoError(t, err)
	assert.Equal(t, payload, string(decompressed))
}

func gzipCompress(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, err := gz.Write([]byte(s))
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	return buf.Bytes()
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
