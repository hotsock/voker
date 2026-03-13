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

	resp := adapter.Response(recorder)

	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "200 OK", resp.StatusDescription)
	assert.Equal(t, "<h1>Hello</h1>", resp.Body)
	assert.False(t, resp.IsBase64Encoded)
}

func TestALBResponse_BinaryBody(t *testing.T) {
	adapter := &ALB{}
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

		resp := adapter.Response(recorder)
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

	resp := adapter.Response(recorder)

	// ALB uses headers map for cookies (no top-level cookies field)
	assert.Equal(t, "session=abc; HttpOnly", resp.Headers["set-cookie"])
}
