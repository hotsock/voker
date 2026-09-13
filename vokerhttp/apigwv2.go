package vokerhttp

import (
	"context"
	"net/http"
	"net/url"
)

// APIGatewayV2 implements [Adapter] for API Gateway v2 HTTP API events
// (payload format 2.0).
//
// HTTP APIs supply a decoded rawPath. The adapter preserves that value as
// URL.Path; it cannot reconstruct escaping or characters removed by AWS.
//
//	vokerhttp.Start(mux, &vokerhttp.APIGatewayV2{})
type APIGatewayV2 struct{}

// APIGatewayV2Request is the API Gateway v2 HTTP API event (payload format
// 2.0). It shares the [PayloadV2Request] shape with Lambda Function URLs but
// is a distinct type so [EventFromContext] can tell the event sources apart.
type APIGatewayV2Request PayloadV2Request

// APIGatewayV2Response is the API Gateway v2 HTTP API response (payload format 2.0).
type APIGatewayV2Response PayloadV2Response

// Request converts an API Gateway v2 event into an *http.Request.
func (a *APIGatewayV2) Request(ctx context.Context, event APIGatewayV2Request) (*http.Request, error) {
	// HTTP APIs deliver a decoded rawPath, unlike Function URLs. Escape it
	// before the shared URL parser so literal %, ?, and # remain path data.
	// Any decoding or truncation already performed by AWS is irreversible.
	event.RawPath = (&url.URL{Path: event.RawPath}).EscapedPath()
	return buildV2Request(ctx, PayloadV2Request(event))
}

// Response converts the handler's *http.Response into an API Gateway v2 response.
func (a *APIGatewayV2) Response(resp *http.Response) (APIGatewayV2Response, error) {
	out, err := buildV2Response(resp)
	return APIGatewayV2Response(out), err
}
