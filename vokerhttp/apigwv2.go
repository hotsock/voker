package vokerhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
)

// APIGatewayV2 implements [Adapter] for API Gateway v2 HTTP API events
// (payload format 2.0).
//
//	vokerhttp.StartHTTP(mux, &vokerhttp.APIGatewayV2{})
type APIGatewayV2 struct{}

// APIGatewayV2Request is the API Gateway v2 HTTP API event (payload format 2.0).
type APIGatewayV2Request = payloadV2Request

// APIGatewayV2Response is the API Gateway v2 HTTP API response (payload format 2.0).
type APIGatewayV2Response = payloadV2Response

// Request converts an API Gateway v2 event into an *http.Request.
func (a *APIGatewayV2) Request(ctx context.Context, event APIGatewayV2Request) (*http.Request, error) {
	return buildV2Request(ctx, event)
}

// Response converts an httptest.ResponseRecorder into an API Gateway v2 response.
func (a *APIGatewayV2) Response(w *httptest.ResponseRecorder) APIGatewayV2Response {
	return buildV2Response(w)
}
