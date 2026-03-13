package voker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
)

type eventContextKey struct{}

// Adapter converts between a Lambda event type and net/http.
// Implement this interface for each Lambda event source.
//
// Voker provides four built-in adapters:
//   - [FunctionURL] for Lambda Function URLs
//   - [APIGatewayV2] for API Gateway v2 HTTP APIs
//   - [APIGatewayV1] for API Gateway v1 REST APIs
//   - [ALB] for Application Load Balancer target groups
type Adapter[E, R any] interface {
	// Request converts a Lambda event into an *http.Request.
	Request(ctx context.Context, event E) (*http.Request, error)

	// Response converts a completed ResponseRecorder into a Lambda response.
	Response(w *httptest.ResponseRecorder) R
}

// StartHTTP starts the Lambda runtime loop with a standard http.Handler
// and an Adapter that handles event format conversion.
//
// The original Lambda event is stored on the request context and can be
// retrieved using [EventFromContext]:
//
//	func handler(w http.ResponseWriter, r *http.Request) {
//	    event, ok := voker.EventFromContext[voker.FunctionURLRequest](r.Context())
//	}
//
// Usage:
//
//	voker.StartHTTP(mux, &voker.FunctionURL{})
//	voker.StartHTTP(mux, &voker.APIGatewayV2{})
//	voker.StartHTTP(mux, &voker.APIGatewayV1{})
//	voker.StartHTTP(mux, &voker.ALB{})
//
// Options can be provided to configure runtime behavior:
//
//	voker.StartHTTP(mux, &voker.FunctionURL{}, voker.WithTraceID(true))
func StartHTTP[E, R any](handler http.Handler, adapter Adapter[E, R], opts ...Option) {
	Start(func(ctx context.Context, event E) (R, error) {
		req, err := adapter.Request(ctx, event)
		if err != nil {
			var zero R
			return zero, fmt.Errorf("failed to build http request: %w", err)
		}

		req = req.WithContext(context.WithValue(req.Context(), eventContextKey{}, event))

		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)

		return adapter.Response(recorder), nil
	}, opts...)
}

// EventFromContext retrieves the original Lambda event from the request context.
// The type parameter must match the event type for the adapter in use:
//
//	event, ok := voker.EventFromContext[voker.FunctionURLRequest](r.Context())
//	event, ok := voker.EventFromContext[voker.APIGatewayV2Request](r.Context())
//	event, ok := voker.EventFromContext[voker.APIGatewayV1Request](r.Context())
//	event, ok := voker.EventFromContext[voker.ALBRequest](r.Context())
func EventFromContext[E any](ctx context.Context) (E, bool) {
	event, ok := ctx.Value(eventContextKey{}).(E)
	return event, ok
}

// isTextContent returns true if the given Content-Type represents text
// content that can be returned as a plain string (not base64-encoded).
func isTextContent(contentType string) bool {
	if contentType == "" {
		return false
	}

	// Strip parameters (e.g. "; charset=utf-8")
	mediaType, _, _ := strings.Cut(contentType, ";")
	mediaType = strings.TrimSpace(strings.ToLower(mediaType))

	if strings.HasPrefix(mediaType, "text/") {
		return true
	}

	switch mediaType {
	case "application/json",
		"application/xml",
		"application/javascript",
		"application/x-www-form-urlencoded":
		return true
	}

	if strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml") {
		return true
	}

	return false
}
