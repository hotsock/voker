package voker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestFunctionURLRequest() FunctionURLRequest {
	return FunctionURLRequest{
		Version:        "2.0",
		RouteKey:       "$default",
		RawPath:        "/my/path",
		RawQueryString: "foo=bar&baz=qux",
		Headers: map[string]string{
			"host":         "abc123.lambda-url.us-east-1.on.aws",
			"content-type": "application/json",
			"x-custom":     "value",
		},
		RequestContext: PayloadV2RequestContext{
			AccountID:  "123456789012",
			APIID:      "abc123",
			DomainName: "abc123.lambda-url.us-east-1.on.aws",
			HTTP: PayloadV2RequestContextHTTP{
				Method:    "GET",
				Path:      "/my/path",
				Protocol:  "HTTP/1.1",
				SourceIP:  "1.2.3.4",
				UserAgent: "TestAgent/1.0",
			},
			RequestID: "req-123",
		},
	}
}

func TestFunctionURLRequest_Basic(t *testing.T) {
	adapter := &FunctionURL{}
	event := newTestFunctionURLRequest()

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, "/my/path", req.URL.Path)
	assert.Equal(t, "foo=bar&baz=qux", req.URL.RawQuery)
	assert.Equal(t, "abc123.lambda-url.us-east-1.on.aws", req.URL.Host)
	assert.Equal(t, "https", req.URL.Scheme)
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Equal(t, "value", req.Header.Get("X-Custom"))
	assert.Equal(t, "1.2.3.4", req.RemoteAddr)
	assert.Equal(t, "/my/path?foo=bar&baz=qux", req.RequestURI)
}

func TestFunctionURLRequest_WithBody(t *testing.T) {
	adapter := &FunctionURL{}
	event := newTestFunctionURLRequest()
	event.RequestContext.HTTP.Method = "POST"
	event.Body = `{"key":"value"}`

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, `{"key":"value"}`, string(body))
}

func TestFunctionURLRequest_Base64Body(t *testing.T) {
	adapter := &FunctionURL{}
	event := newTestFunctionURLRequest()
	event.RequestContext.HTTP.Method = "POST"
	event.Body = base64.StdEncoding.EncodeToString([]byte("binary data"))
	event.IsBase64Encoded = true

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, "binary data", string(body))
}

func TestFunctionURLRequest_EmptyQueryString(t *testing.T) {
	adapter := &FunctionURL{}
	event := newTestFunctionURLRequest()
	event.RawQueryString = ""

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "", req.URL.RawQuery)
	assert.Equal(t, "/my/path", req.RequestURI)
}

func TestFunctionURLRequest_Cookies(t *testing.T) {
	adapter := &FunctionURL{}
	event := newTestFunctionURLRequest()
	event.Cookies = []string{"session=abc123", "theme=dark"}

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	cookies := req.Cookies()
	require.Len(t, cookies, 2)
	assert.Equal(t, "session", cookies[0].Name)
	assert.Equal(t, "abc123", cookies[0].Value)
	assert.Equal(t, "theme", cookies[1].Name)
	assert.Equal(t, "dark", cookies[1].Value)
}

func TestFunctionURLRequest_ContextPropagation(t *testing.T) {
	adapter := &FunctionURL{}
	event := newTestFunctionURLRequest()

	lc := &LambdaContext{
		AwsRequestID:       "req-456",
		InvokedFunctionArn: "arn:aws:lambda:us-east-1:123:function:test",
	}
	ctx := NewContext(context.Background(), lc)

	req, err := adapter.Request(ctx, event)
	require.NoError(t, err)

	gotLC, ok := FromContext(req.Context())
	require.True(t, ok)
	assert.Equal(t, "req-456", gotLC.AwsRequestID)
	assert.Equal(t, "arn:aws:lambda:us-east-1:123:function:test", gotLC.InvokedFunctionArn)
}

func TestFunctionURLResponse_TextBody(t *testing.T) {
	adapter := &FunctionURL{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/html")
	recorder.WriteHeader(http.StatusOK)
	recorder.Write([]byte("<h1>Hello</h1>"))

	resp := adapter.Response(recorder)

	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "<h1>Hello</h1>", resp.Body)
	assert.False(t, resp.IsBase64Encoded)
}

func TestFunctionURLResponse_JSONBody(t *testing.T) {
	adapter := &FunctionURL{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "application/json")
	recorder.WriteHeader(http.StatusOK)
	recorder.Write([]byte(`{"ok":true}`))

	resp := adapter.Response(recorder)

	assert.Equal(t, `{"ok":true}`, resp.Body)
	assert.False(t, resp.IsBase64Encoded)
}

func TestFunctionURLResponse_BinaryBody(t *testing.T) {
	adapter := &FunctionURL{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "application/octet-stream")
	recorder.WriteHeader(http.StatusOK)
	data := []byte{0x00, 0x01, 0x02, 0xFF}
	recorder.Write(data)

	resp := adapter.Response(recorder)

	assert.True(t, resp.IsBase64Encoded)
	decoded, err := base64.StdEncoding.DecodeString(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, data, decoded)
}

func TestFunctionURLResponse_NoContentType(t *testing.T) {
	adapter := &FunctionURL{}
	recorder := httptest.NewRecorder()
	recorder.WriteHeader(http.StatusOK)
	recorder.Write([]byte("unknown content"))

	resp := adapter.Response(recorder)

	assert.True(t, resp.IsBase64Encoded)
}

func TestFunctionURLResponse_Cookies(t *testing.T) {
	adapter := &FunctionURL{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/plain")
	recorder.Header().Add("Set-Cookie", "session=abc; HttpOnly")
	recorder.Header().Add("Set-Cookie", "theme=dark; Path=/")
	recorder.WriteHeader(http.StatusOK)
	recorder.Write([]byte("ok"))

	resp := adapter.Response(recorder)

	assert.Len(t, resp.Cookies, 2)
	assert.Contains(t, resp.Cookies, "session=abc; HttpOnly")
	assert.Contains(t, resp.Cookies, "theme=dark; Path=/")
	// Set-Cookie should not appear in the headers map
	_, hasCookie := resp.Headers["set-cookie"]
	assert.False(t, hasCookie)
}

func TestFunctionURLResponse_StatusCode(t *testing.T) {
	adapter := &FunctionURL{}

	for _, code := range []int{200, 201, 301, 404, 500} {
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Content-Type", "text/plain")
		recorder.WriteHeader(code)

		resp := adapter.Response(recorder)
		assert.Equal(t, code, resp.StatusCode)
	}
}

func TestFunctionURLResponse_MultiValueHeaders(t *testing.T) {
	adapter := &FunctionURL{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/plain")
	recorder.Header().Add("X-Custom", "val1")
	recorder.Header().Add("X-Custom", "val2")
	recorder.WriteHeader(http.StatusOK)
	recorder.Write([]byte("ok"))

	resp := adapter.Response(recorder)

	assert.Equal(t, "val1, val2", resp.Headers["x-custom"])
}

// Integration test: send a Function URL JSON event through the full
// handleInvocation path and verify the response.
func TestFunctionURL_Integration(t *testing.T) {
	var capturedResponse FunctionURLResponse

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2018-06-01/runtime/invocation/next":
			w.Header().Set(headerRequestID, "int-req-1")
			w.Header().Set(headerDeadlineMS, "999999999999999")
			w.Header().Set(headerFunctionARN, "arn:aws:lambda:us-east-1:123:function:test")
			w.WriteHeader(http.StatusOK)

			event := FunctionURLRequest{
				Version:        "2.0",
				RouteKey:       "$default",
				RawPath:        "/hello",
				RawQueryString: "name=world",
				Headers: map[string]string{
					"host":         "test.lambda-url.us-east-1.on.aws",
					"content-type": "text/plain",
				},
				RequestContext: PayloadV2RequestContext{
					AccountID: "123456789012",
					APIID:     "abc123",
					HTTP: PayloadV2RequestContextHTTP{
						Method:   "GET",
						Path:     "/hello",
						Protocol: "HTTP/1.1",
						SourceIP: "10.0.0.1",
					},
					RequestID: "int-req-1",
				},
			}
			json.NewEncoder(w).Encode(event)

		case "/2018-06-01/runtime/invocation/int-req-1/response":
			err := json.NewDecoder(r.Body).Decode(&capturedResponse)
			require.NoError(t, err)
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)

	adapter := &FunctionURL{}
	httpHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/hello", r.URL.Path)
		assert.Equal(t, "name=world", r.URL.RawQuery)
		assert.Equal(t, "10.0.0.1", r.RemoteAddr)

		lc, ok := FromContext(r.Context())
		assert.True(t, ok)
		assert.Equal(t, "int-req-1", lc.AwsRequestID)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"greeting":"hello world"}`))
	})

	handler := func(ctx context.Context, event FunctionURLRequest) (FunctionURLResponse, error) {
		req, err := adapter.Request(ctx, event)
		if err != nil {
			return FunctionURLResponse{StatusCode: 500}, err
		}
		recorder := httptest.NewRecorder()
		httpHandler.ServeHTTP(recorder, req)
		return adapter.Response(recorder), nil
	}

	err := handleInvocation(client, handler, &options{logger: logger})
	require.NoError(t, err)

	assert.Equal(t, 200, capturedResponse.StatusCode)
	assert.Equal(t, `{"greeting":"hello world"}`, capturedResponse.Body)
	assert.False(t, capturedResponse.IsBase64Encoded)
	assert.Equal(t, "application/json", capturedResponse.Headers["content-type"])
}
