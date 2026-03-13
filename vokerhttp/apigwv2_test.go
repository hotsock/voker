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

func newTestAPIGatewayV2Request() APIGatewayV2Request {
	return APIGatewayV2Request{
		Version:        "2.0",
		RouteKey:       "$default",
		RawPath:        "/my/path",
		RawQueryString: "foo=bar&baz=qux",
		Headers: map[string]string{
			"host":         "abc123.execute-api.us-east-1.amazonaws.com",
			"content-type": "application/json",
			"x-custom":     "value",
		},
		RequestContext: PayloadV2RequestContext{
			AccountID:  "123456789012",
			APIID:      "abc123",
			DomainName: "abc123.execute-api.us-east-1.amazonaws.com",
			HTTP: PayloadV2RequestContextHTTP{
				Method:    "GET",
				Path:      "/my/path",
				Protocol:  "HTTP/1.1",
				SourceIP:  "1.2.3.4",
				UserAgent: "TestAgent/1.0",
			},
			RequestID: "req-123",
			Stage:     "$default",
		},
	}
}

func TestAPIGatewayV2Request_Basic(t *testing.T) {
	adapter := &APIGatewayV2{}
	event := newTestAPIGatewayV2Request()

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, "/my/path", req.URL.Path)
	assert.Equal(t, "foo=bar&baz=qux", req.URL.RawQuery)
	assert.Equal(t, "abc123.execute-api.us-east-1.amazonaws.com", req.URL.Host)
	assert.Equal(t, "https", req.URL.Scheme)
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Equal(t, "value", req.Header.Get("X-Custom"))
	assert.Equal(t, "1.2.3.4", req.RemoteAddr)
	assert.Equal(t, "/my/path?foo=bar&baz=qux", req.RequestURI)
}

func TestAPIGatewayV2Request_WithBody(t *testing.T) {
	adapter := &APIGatewayV2{}
	event := newTestAPIGatewayV2Request()
	event.RequestContext.HTTP.Method = "POST"
	event.Body = `{"key":"value"}`

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, `{"key":"value"}`, string(body))
}

func TestAPIGatewayV2Request_Base64Body(t *testing.T) {
	adapter := &APIGatewayV2{}
	event := newTestAPIGatewayV2Request()
	event.RequestContext.HTTP.Method = "POST"
	event.Body = base64.StdEncoding.EncodeToString([]byte("binary data"))
	event.IsBase64Encoded = true

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, "binary data", string(body))
}

func TestAPIGatewayV2Request_EmptyQueryString(t *testing.T) {
	adapter := &APIGatewayV2{}
	event := newTestAPIGatewayV2Request()
	event.RawQueryString = ""

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "", req.URL.RawQuery)
	assert.Equal(t, "/my/path", req.RequestURI)
}

func TestAPIGatewayV2Request_Cookies(t *testing.T) {
	adapter := &APIGatewayV2{}
	event := newTestAPIGatewayV2Request()
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

func TestAPIGatewayV2Request_ContextPropagation(t *testing.T) {
	adapter := &APIGatewayV2{}
	event := newTestAPIGatewayV2Request()

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
	assert.Equal(t, "arn:aws:lambda:us-east-1:123:function:test", gotLC.InvokedFunctionArn)
}

func TestAPIGatewayV2Response_TextBody(t *testing.T) {
	adapter := &APIGatewayV2{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/html")
	recorder.WriteHeader(http.StatusOK)
	recorder.Write([]byte("<h1>Hello</h1>"))

	resp := adapter.Response(recorder)

	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "<h1>Hello</h1>", resp.Body)
	assert.False(t, resp.IsBase64Encoded)
}

func TestAPIGatewayV2Response_BinaryBody(t *testing.T) {
	adapter := &APIGatewayV2{}
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

func TestAPIGatewayV2Response_Cookies(t *testing.T) {
	adapter := &APIGatewayV2{}
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
	_, hasCookie := resp.Headers["set-cookie"]
	assert.False(t, hasCookie)
}

func TestAPIGatewayV2Response_MultiValueHeaders(t *testing.T) {
	adapter := &APIGatewayV2{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/plain")
	recorder.Header().Add("X-Custom", "val1")
	recorder.Header().Add("X-Custom", "val2")
	recorder.WriteHeader(http.StatusOK)
	recorder.Write([]byte("ok"))

	resp := adapter.Response(recorder)

	assert.Equal(t, "val1, val2", resp.Headers["x-custom"])
}
