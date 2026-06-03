// Package vokerhttp provides adapters that convert Lambda events into
// standard net/http requests. It wraps [voker.Start] so users can pass
// an http.Handler instead of a typed Lambda handler.
//
// Usage:
//
//	vokerhttp.StartHTTP(mux, &vokerhttp.FunctionURL{})
//	vokerhttp.StartHTTP(mux, &vokerhttp.APIGatewayV2{})
//	vokerhttp.StartHTTP(mux, &vokerhttp.APIGatewayV1{})
//	vokerhttp.StartHTTP(mux, &vokerhttp.ALB{})
package vokerhttp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/hotsock/voker"
)

type eventContextKey struct{}

// Adapter converts between a Lambda event type and net/http.
// Implement this interface for each Lambda event source.
//
// Vokerhttp provides four built-in adapters:
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
//	    event, ok := vokerhttp.EventFromContext[vokerhttp.FunctionURLRequest](r.Context())
//	}
//
// Usage:
//
//	vokerhttp.StartHTTP(mux, &vokerhttp.FunctionURL{})
//	vokerhttp.StartHTTP(mux, &vokerhttp.APIGatewayV2{}, voker.WithLogger(logger))
func StartHTTP[E, R any](handler http.Handler, adapter Adapter[E, R], opts ...voker.Option) {
	voker.Start(eventHandler(handler, adapter), opts...)
}

// eventHandler builds the typed Lambda handler that StartHTTP passes to
// [voker.Start]. It is kept separate from StartHTTP so the event-to-request
// conversion, context propagation, and response conversion can be exercised
// without running the blocking runtime loop.
func eventHandler[E, R any](handler http.Handler, adapter Adapter[E, R]) func(context.Context, E) (R, error) {
	return func(ctx context.Context, event E) (R, error) {
		req, err := adapter.Request(ctx, event)
		if err != nil {
			var zero R
			return zero, fmt.Errorf("failed to build http request: %w", err)
		}

		req = req.WithContext(context.WithValue(req.Context(), eventContextKey{}, event))

		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)

		return adapter.Response(recorder), nil
	}
}

// EventFromContext retrieves the original Lambda event from the request context.
// The type parameter must match the event type for the adapter in use:
//
//	event, ok := vokerhttp.EventFromContext[vokerhttp.FunctionURLRequest](r.Context())
//	event, ok := vokerhttp.EventFromContext[vokerhttp.APIGatewayV2Request](r.Context())
//	event, ok := vokerhttp.EventFromContext[vokerhttp.APIGatewayV1Request](r.Context())
//	event, ok := vokerhttp.EventFromContext[vokerhttp.ALBRequest](r.Context())
func EventFromContext[E any](ctx context.Context) (E, bool) {
	event, ok := ctx.Value(eventContextKey{}).(E)
	return event, ok
}

func responseStatusCode(w *httptest.ResponseRecorder) int {
	return w.Result().StatusCode
}

func mergedQueryValues(single map[string]string, multi map[string][]string) url.Values {
	params := url.Values{}
	for k, v := range single {
		params.Set(k, v)
	}
	for k, vals := range multi {
		params.Del(k)
		for _, v := range vals {
			params.Add(k, v)
		}
	}
	return params
}

func addMergedHeaders(req *http.Request, single map[string]string, multi map[string][]string) {
	for k, v := range single {
		req.Header.Set(k, v)
	}
	for k, vals := range multi {
		req.Header.Del(k)
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
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
