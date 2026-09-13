package vokerhttp

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reduced live AWS captures from examples/aws-ingress-probe/paths.py.
// The encoded path is preserved by REST, ALB, and Function URLs; HTTP APIs
// deliver decoded (and sometimes already truncated/double-decoded) paths.
func TestLiveIngressPaths(t *testing.T) {
	var captures []struct {
		SentPath        string `json:"sentPath"`
		HTTPAPIPath     string `json:"httpAPIPath"`
		HTTPAPIRejected bool   `json:"httpAPIRejected"`
	}
	readEventFixture(t, "ingress-paths.json", &captures)
	query := "marker=path-probe&value=a%3Fb%25c%23d&repeat=one&repeat=two"
	params, err := url.ParseQuery(query)
	require.NoError(t, err)
	ctx := context.Background()
	for _, capture := range captures {
		t.Run(capture.SentPath, func(t *testing.T) {
			decoded, err := url.PathUnescape(capture.SentPath)
			require.NoError(t, err)
			check := func(t *testing.T, req *http.Request, err error, want string) {
				t.Helper()
				require.NoError(t, err)
				defer req.Body.Close()
				assert.Equal(t, want, req.URL.Path)
				assert.Empty(t, req.URL.Fragment)
				assert.Equal(t, params, req.URL.Query())
				uri, err := url.ParseRequestURI(req.RequestURI)
				require.NoError(t, err)
				assert.Equal(t, want, uri.Path)
				assert.Equal(t, params, uri.Query())
			}
			headers := map[string]string{"host": "example.com"}
			t.Run("function-url", func(t *testing.T) {
				event := FunctionURLRequest{RawPath: capture.SentPath, RawQueryString: query, Headers: headers}
				event.RequestContext.HTTP.Method = "GET"
				req, err := (&FunctionURL{}).Request(ctx, event)
				check(t, req, err, decoded)
			})
			t.Run("rest", func(t *testing.T) {
				req, err := (&APIGatewayV1{}).Request(ctx, APIGatewayV1Request{Path: capture.SentPath, HTTPMethod: "GET", Headers: headers, MultiValueQueryStringParameters: params})
				check(t, req, err, decoded)
			})
			t.Run("alb", func(t *testing.T) {
				// ALB query fields remain encoded, unlike REST query fields.
				req, err := (&ALB{}).Request(ctx, ALBRequest{Path: capture.SentPath, HTTPMethod: "GET", Headers: headers, MultiValueQueryStringParameters: map[string][]string{"marker": {"path-probe"}, "value": {"a%3Fb%25c%23d"}, "repeat": {"one", "two"}}})
				check(t, req, err, decoded)
			})
			if !capture.HTTPAPIRejected {
				t.Run("http-api", func(t *testing.T) {
					event := APIGatewayV2Request{RawPath: capture.HTTPAPIPath, RawQueryString: query, Headers: headers}
					event.RequestContext.HTTP.Method = "GET"
					req, err := (&APIGatewayV2{}).Request(ctx, event)
					check(t, req, err, capture.HTTPAPIPath)
				})
			}
		})
	}
}
