package vokerhttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hotsock/voker"
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

func TestFunctionURLRequest_AWSDocumentedJSONFixture(t *testing.T) {
	const fixture = `{
		"version": "2.0",
		"routeKey": "$default",
		"rawPath": "/my/path",
		"rawQueryString": "parameter1=value1&parameter1=value2&parameter2=value",
		"cookies": ["Cookie_1=Value_1", "Cookie_2=Value_2"],
		"headers": {
			"header1": "value1",
			"header2": "value1,value2",
			"host": "abc123.lambda-url.us-east-1.on.aws"
		},
		"queryStringParameters": {
			"parameter1": "value1,value2",
			"parameter2": "value"
		},
		"requestContext": {
			"accountId": "123456789012",
			"apiId": "abc123",
			"domainName": "abc123.lambda-url.us-east-1.on.aws",
			"domainPrefix": "abc123",
			"http": {
				"method": "GET",
				"path": "/my/path",
				"protocol": "HTTP/1.1",
				"sourceIp": "192.0.2.1",
				"userAgent": "agent"
			},
			"requestId": "id",
			"routeKey": "$default",
			"stage": "$default",
			"time": "12/Mar/2020:19:03:58 +0000",
			"timeEpoch": 1583348638390
		},
		"body": "Hello from Lambda",
		"isBase64Encoded": false
	}`

	var event FunctionURLRequest
	require.NoError(t, json.Unmarshal([]byte(fixture), &event))

	req, err := (&FunctionURL{}).Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, "/my/path", req.URL.Path)
	assert.Equal(t, "parameter1=value1&parameter1=value2&parameter2=value", req.URL.RawQuery)
	assert.Equal(t, "abc123.lambda-url.us-east-1.on.aws", req.URL.Host)
	cookies := req.Cookies()
	require.Len(t, cookies, 2)
	assert.Equal(t, "Cookie_1", cookies[0].Name)
	assert.Equal(t, "Value_1", cookies[0].Value)
	assert.Equal(t, "Cookie_2", cookies[1].Name)
	assert.Equal(t, "Value_2", cookies[1].Value)
}

func TestFunctionURLRequest_DomainNameFallbackHost(t *testing.T) {
	adapter := &FunctionURL{}
	event := newTestFunctionURLRequest()
	delete(event.Headers, "host")

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "abc123.lambda-url.us-east-1.on.aws", req.URL.Host)
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

func TestFunctionURLRequest_InvalidBase64Body(t *testing.T) {
	adapter := &FunctionURL{}
	event := newTestFunctionURLRequest()
	event.RequestContext.HTTP.Method = "POST"
	event.Body = "not!valid!base64!"
	event.IsBase64Encoded = true

	_, err := adapter.Request(context.Background(), event)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode base64 body")
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

func TestFunctionURLRequest_CookieValuesPreservedVerbatim(t *testing.T) {
	// Cookie values containing spaces or commas must reach the handler
	// exactly as the client sent them, without sanitization or quoting.
	adapter := &FunctionURL{}
	event := newTestFunctionURLRequest()
	event.Cookies = []string{"session=a b c", "pair=x,y"}

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "session=a b c; pair=x,y", req.Header.Get("Cookie"))
}

func TestFunctionURLRequest_ContextPropagation(t *testing.T) {
	adapter := &FunctionURL{}
	event := newTestFunctionURLRequest()

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

func TestFunctionURLResponse_TextBody(t *testing.T) {
	adapter := &FunctionURL{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/html")
	recorder.WriteHeader(http.StatusOK)
	recorder.Write([]byte("<h1>Hello</h1>"))

	resp, err := adapter.Response(recorder.Result())
	require.NoError(t, err)

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

	resp, err := adapter.Response(recorder.Result())
	require.NoError(t, err)

	assert.Equal(t, `{"ok":true}`, resp.Body)
	assert.False(t, resp.IsBase64Encoded)
}

func TestFunctionURLResponse_ImplicitStatusOK(t *testing.T) {
	adapter := &FunctionURL{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/plain")

	resp, err := adapter.Response(recorder.Result())
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.False(t, resp.IsBase64Encoded)
}

func TestFunctionURLResponse_BinaryBody(t *testing.T) {
	adapter := &FunctionURL{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "application/octet-stream")
	recorder.WriteHeader(http.StatusOK)
	data := []byte{0x00, 0x01, 0x02, 0xFF}
	recorder.Write(data)

	resp, err := adapter.Response(recorder.Result())
	require.NoError(t, err)

	assert.True(t, resp.IsBase64Encoded)
	decoded, err := base64.StdEncoding.DecodeString(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, data, decoded)
}

func TestFunctionURLResponse_NoContentTypeSniffsText(t *testing.T) {
	// Real net/http servers sniff a Content-Type on the first Write when the
	// handler didn't set one; the adapter must do the same.
	adapter := &FunctionURL{}
	recorder := httptest.NewRecorder()
	recorder.WriteHeader(http.StatusOK)
	recorder.Write([]byte("plain text response"))

	resp, err := adapter.Response(recorder.Result())
	require.NoError(t, err)

	assert.False(t, resp.IsBase64Encoded)
	assert.Equal(t, "plain text response", resp.Body)
	assert.Equal(t, "text/plain; charset=utf-8", resp.Headers["content-type"])
}

func TestFunctionURLResponse_NoContentTypeSniffsBinary(t *testing.T) {
	adapter := &FunctionURL{}
	recorder := httptest.NewRecorder()
	recorder.WriteHeader(http.StatusOK)
	data := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	recorder.Write(data)

	resp, err := adapter.Response(recorder.Result())
	require.NoError(t, err)

	assert.True(t, resp.IsBase64Encoded)
	decoded, err := base64.StdEncoding.DecodeString(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, data, decoded)
	assert.Equal(t, "image/png", resp.Headers["content-type"])
}

func TestFunctionURLResponse_Cookies(t *testing.T) {
	adapter := &FunctionURL{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/plain")
	recorder.Header().Add("Set-Cookie", "session=abc; HttpOnly")
	recorder.Header().Add("Set-Cookie", "theme=dark; Path=/")
	recorder.WriteHeader(http.StatusOK)
	recorder.Write([]byte("ok"))

	resp, err := adapter.Response(recorder.Result())
	require.NoError(t, err)

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

		resp, err := adapter.Response(recorder.Result())
		require.NoError(t, err)
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

	resp, err := adapter.Response(recorder.Result())
	require.NoError(t, err)

	assert.Equal(t, "val1, val2", resp.Headers["x-custom"])
}
