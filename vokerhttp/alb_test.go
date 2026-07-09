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

func newTestALBRequest() ALBRequest {
	return ALBRequest{
		RequestContext: struct {
			ELB struct {
				TargetGroupArn string `json:"targetGroupArn"`
			} `json:"elb"`
		}{
			ELB: struct {
				TargetGroupArn string `json:"targetGroupArn"`
			}{
				TargetGroupArn: "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/tg/abc123",
			},
		},
		HTTPMethod: "GET",
		Path:       "/my/path",
		QueryStringParameters: map[string]string{
			"foo": "bar",
		},
		Headers: map[string]string{
			"host":              "my-alb-123.us-east-1.elb.amazonaws.com",
			"content-type":      "application/json",
			"x-forwarded-for":   "72.21.198.66",
			"x-forwarded-port":  "443",
			"x-forwarded-proto": "https",
		},
	}
}

func TestALBRequest_Basic(t *testing.T) {
	adapter := &ALB{}
	event := newTestALBRequest()

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, "/my/path", req.URL.Path)
	assert.Equal(t, "bar", req.URL.Query().Get("foo"))
	assert.Equal(t, "my-alb-123.us-east-1.elb.amazonaws.com", req.URL.Host)
	assert.Equal(t, "https", req.URL.Scheme)
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Equal(t, "72.21.198.66", req.RemoteAddr)
}

func TestALBRequest_AWSDocumentedJSONFixture(t *testing.T) {
	const fixture = `{
		"requestContext": {
			"elb": {
				"targetGroupArn": "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/lambda-tg/abcdef123456"
			}
		},
		"httpMethod": "GET",
		"path": "/lambda",
		"queryStringParameters": {
			"name": "lambda"
		},
		"headers": {
			"host": "example.com",
			"x-forwarded-for": "192.0.2.1",
			"x-forwarded-port": "443",
			"x-forwarded-proto": "https"
		},
		"body": "",
		"isBase64Encoded": false
	}`

	var event ALBRequest
	require.NoError(t, json.Unmarshal([]byte(fixture), &event))

	req, err := (&ALB{}).Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, "/lambda", req.URL.Path)
	assert.Equal(t, "name=lambda", req.URL.RawQuery)
	assert.Equal(t, "example.com", req.URL.Host)
	assert.Equal(t, "192.0.2.1", req.RemoteAddr)
}

func TestALBRequest_WithBody(t *testing.T) {
	adapter := &ALB{}
	event := newTestALBRequest()
	event.HTTPMethod = "POST"
	event.Body = `{"key":"value"}`

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, `{"key":"value"}`, string(body))
}

func TestALBRequest_Base64Body(t *testing.T) {
	adapter := &ALB{}
	event := newTestALBRequest()
	event.HTTPMethod = "POST"
	event.Body = base64.StdEncoding.EncodeToString([]byte("binary data"))
	event.IsBase64Encoded = true

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, "binary data", string(body))
}

func TestALBRequest_NoQueryString(t *testing.T) {
	adapter := &ALB{}
	event := newTestALBRequest()
	event.QueryStringParameters = nil

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "", req.URL.RawQuery)
	assert.Equal(t, "/my/path", req.RequestURI)
}

func TestALBRequest_MultipleForwardedIPs(t *testing.T) {
	adapter := &ALB{}
	event := newTestALBRequest()
	event.Headers["x-forwarded-for"] = "72.21.198.66, 10.0.0.1, 192.168.1.1"

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "72.21.198.66", req.RemoteAddr)
}

func TestALBRequest_InvalidBase64Body(t *testing.T) {
	adapter := &ALB{}
	event := newTestALBRequest()
	event.HTTPMethod = "POST"
	event.Body = "not!valid!base64!"
	event.IsBase64Encoded = true

	_, err := adapter.Request(context.Background(), event)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode base64 body")
}

func TestALBRequest_MultiValueHeadersAndQuery(t *testing.T) {
	adapter := &ALB{}
	event := newTestALBRequest()
	// When multi-value headers are enabled, ALB sends only the multi-value maps.
	event.Headers = nil
	event.QueryStringParameters = nil
	event.MultiValueHeaders = map[string][]string{
		"host":              {"my-alb-123.us-east-1.elb.amazonaws.com"},
		"x-forwarded-proto": {"https"},
		"x-forwarded-for":   {"72.21.198.66, 10.0.0.1"},
		"accept":            {"text/html", "application/json"},
	}
	event.MultiValueQueryStringParameters = map[string][]string{
		"color": {"red", "blue"},
	}

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "my-alb-123.us-east-1.elb.amazonaws.com", req.URL.Host)
	assert.Equal(t, "https", req.URL.Scheme)
	assert.Equal(t, "72.21.198.66", req.RemoteAddr)
	assert.Equal(t, []string{"text/html", "application/json"}, req.Header.Values("Accept"))
	assert.ElementsMatch(t, []string{"red", "blue"}, req.URL.Query()["color"])
}

func TestALBRequest_AWSMultiValueJSONFixture(t *testing.T) {
	const fixture = `{
		"requestContext": {
			"elb": {
				"targetGroupArn": "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/lambda-tg/abcdef123456"
			}
		},
		"httpMethod": "GET",
		"path": "/lambda",
		"multiValueQueryStringParameters": {
			"name": ["lambda"],
			"color": ["red", "blue"]
		},
		"multiValueHeaders": {
			"host": ["example.com"],
			"x-forwarded-for": ["192.0.2.1"],
			"x-forwarded-port": ["443"],
			"x-forwarded-proto": ["https"],
			"accept": ["text/html", "application/json"]
		},
		"body": "",
		"isBase64Encoded": false
	}`

	var event ALBRequest
	require.NoError(t, json.Unmarshal([]byte(fixture), &event))

	req, err := (&ALB{}).Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "example.com", req.URL.Host)
	assert.Equal(t, []string{"text/html", "application/json"}, req.Header.Values("Accept"))
	assert.ElementsMatch(t, []string{"red", "blue"}, req.URL.Query()["color"])
	assert.Equal(t, "192.0.2.1", req.RemoteAddr)
}

func TestALBRequest_SchemeFallbackAndMissingHost(t *testing.T) {
	adapter := &ALB{}
	event := newTestALBRequest()
	// No x-forwarded-proto and no host: scheme defaults to https, host empty.
	delete(event.Headers, "x-forwarded-proto")
	delete(event.Headers, "host")

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "https", req.URL.Scheme)
	assert.Equal(t, "", req.URL.Host)
}

func TestALBRequest_HTTPScheme(t *testing.T) {
	adapter := &ALB{}
	event := newTestALBRequest()
	event.Headers["x-forwarded-proto"] = "http"

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "http", req.URL.Scheme)
}

func TestALBRequest_PreservesEncodedQueryParameters(t *testing.T) {
	adapter := &ALB{}
	event := newTestALBRequest()
	event.QueryStringParameters = map[string]string{
		"redirect": "https%3A%2F%2Fexample.com%2Fa%2Fb",
		"space":    "a%20b",
	}

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "redirect=https%3A%2F%2Fexample.com%2Fa%2Fb&space=a%20b", req.URL.RawQuery)
	assert.NotContains(t, req.URL.RawQuery, "%252F")
	assert.Equal(t, "https://example.com/a/b", req.URL.Query().Get("redirect"))
	assert.Equal(t, "a b", req.URL.Query().Get("space"))
}

func TestALBRequest_RawQueryEdgeCases(t *testing.T) {
	adapter := &ALB{}
	event := newTestALBRequest()
	event.QueryStringParameters = map[string]string{
		"empty":       "",
		"encodedPlus": "a%2Bb",
		"embedded":    "a%26b",
		"literalPlus": "a+b",
	}

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "embedded=a%26b&empty=&encodedPlus=a%2Bb&literalPlus=a+b", req.URL.RawQuery)
	assert.Equal(t, "a&b", req.URL.Query().Get("embedded"))
	assert.Equal(t, "", req.URL.Query().Get("empty"))
	assert.Equal(t, "a+b", req.URL.Query().Get("encodedPlus"))
	assert.Equal(t, "a b", req.URL.Query().Get("literalPlus"))
}

func TestALBRequest_PreservesMultiValueRawQueryParameters(t *testing.T) {
	adapter := &ALB{}
	event := newTestALBRequest()
	event.QueryStringParameters = nil
	event.MultiValueQueryStringParameters = map[string][]string{
		"color": {"red", "blue"},
		"empty": {""},
	}

	req, err := adapter.Request(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, "color=red&color=blue&empty=", req.URL.RawQuery)
	assert.ElementsMatch(t, []string{"red", "blue"}, req.URL.Query()["color"])
	assert.Equal(t, "", req.URL.Query().Get("empty"))
}

func TestALBRequest_ContextPropagation(t *testing.T) {
	adapter := &ALB{}
	event := newTestALBRequest()

	lc := &voker.LambdaContext{
		AwsRequestID:       "req-789",
		InvokedFunctionArn: "arn:aws:lambda:us-east-1:123:function:test",
	}
	ctx := voker.NewContext(context.Background(), lc)

	req, err := adapter.Request(ctx, event)
	require.NoError(t, err)

	gotLC, ok := voker.FromContext(req.Context())
	require.True(t, ok)
	assert.Equal(t, "req-789", gotLC.AwsRequestID)
}

func TestALBResponse_TextBody(t *testing.T) {
	adapter := &ALB{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/html")
	recorder.WriteHeader(http.StatusOK)
	recorder.Write([]byte("<h1>Hello</h1>"))

	resp, err := adapter.Response(recorder.Result())
	require.NoError(t, err)

	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "200 OK", resp.StatusDescription)
	assert.Equal(t, "<h1>Hello</h1>", resp.Body)
	assert.False(t, resp.IsBase64Encoded)
}

func TestALBResponse_ImplicitStatusOK(t *testing.T) {
	adapter := &ALB{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/plain")

	resp, err := adapter.Response(recorder.Result())
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "200 OK", resp.StatusDescription)
	assert.False(t, resp.IsBase64Encoded)
}

func TestALBResponse_BinaryBody(t *testing.T) {
	adapter := &ALB{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "image/png")
	recorder.WriteHeader(http.StatusOK)
	data := []byte{0x89, 0x50, 0x4E, 0x47}
	recorder.Write(data)

	resp, err := adapter.Response(recorder.Result())
	require.NoError(t, err)

	assert.True(t, resp.IsBase64Encoded)
	decoded, err := base64.StdEncoding.DecodeString(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, data, decoded)
}

func TestALBResponse_NoContentTypeSniffsText(t *testing.T) {
	adapter := &ALB{}
	recorder := httptest.NewRecorder()
	recorder.WriteHeader(http.StatusOK)
	recorder.Write([]byte("plain text response"))

	resp, err := adapter.Response(recorder.Result())
	require.NoError(t, err)

	assert.False(t, resp.IsBase64Encoded)
	assert.Equal(t, "plain text response", resp.Body)
	assert.Equal(t, "text/plain; charset=utf-8", resp.Headers["content-type"])
}

func TestALBResponse_SingleValueHeadersLastValueWins(t *testing.T) {
	// The ALB single-value headers format cannot represent repeated headers;
	// only the last value of each is kept (see ALB.MultiValueHeaders docs).
	adapter := &ALB{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/plain")
	recorder.Header().Add("Set-Cookie", "session=abc; HttpOnly")
	recorder.Header().Add("Set-Cookie", "theme=dark; Path=/")
	recorder.WriteHeader(http.StatusOK)
	recorder.Write([]byte("ok"))

	resp, err := adapter.Response(recorder.Result())
	require.NoError(t, err)

	assert.Equal(t, "theme=dark; Path=/", resp.Headers["set-cookie"])
	assert.Nil(t, resp.MultiValueHeaders)
}

func TestALBResponse_StatusDescription(t *testing.T) {
	adapter := &ALB{}

	tests := []struct {
		code int
		desc string
	}{
		{200, "200 OK"},
		{201, "201 Created"},
		{301, "301 Moved Permanently"},
		{404, "404 Not Found"},
		{500, "500 Internal Server Error"},
	}

	for _, tt := range tests {
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Content-Type", "text/plain")
		recorder.WriteHeader(tt.code)

		resp, err := adapter.Response(recorder.Result())
		require.NoError(t, err)
		assert.Equal(t, tt.desc, resp.StatusDescription)
	}
}

func TestALBResponse_SetCookieInHeaders(t *testing.T) {
	adapter := &ALB{}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/plain")
	recorder.Header().Set("Set-Cookie", "session=abc; HttpOnly")
	recorder.WriteHeader(http.StatusOK)
	recorder.Write([]byte("ok"))

	resp, err := adapter.Response(recorder.Result())
	require.NoError(t, err)

	// ALB uses headers map for cookies (no top-level cookies field)
	assert.Equal(t, "session=abc; HttpOnly", resp.Headers["set-cookie"])
}

func TestALBResponse_MultiValueHeaders(t *testing.T) {
	adapter := &ALB{MultiValueHeaders: true}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/plain")
	recorder.Header().Add("Set-Cookie", "session=abc; HttpOnly")
	recorder.Header().Add("Set-Cookie", "theme=dark; Path=/")
	recorder.Header().Add("X-Custom", "val1")
	recorder.Header().Add("X-Custom", "val2")
	recorder.WriteHeader(http.StatusOK)
	recorder.Write([]byte("ok"))

	resp, err := adapter.Response(recorder.Result())
	require.NoError(t, err)

	assert.Nil(t, resp.Headers)
	assert.Equal(t, []string{"session=abc; HttpOnly", "theme=dark; Path=/"}, resp.MultiValueHeaders["set-cookie"])
	assert.Equal(t, []string{"val1", "val2"}, resp.MultiValueHeaders["x-custom"])
}
