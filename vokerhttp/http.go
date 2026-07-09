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
	"encoding/base64"
	"fmt"
	"io"
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

	// Response converts the *http.Response produced by the http.Handler into
	// a Lambda response. The response body is a stream; implementations that
	// need the full body must read it and are responsible for closing it.
	Response(resp *http.Response) (R, error)
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

		response, err := adapter.Response(recorder.Result())
		if err != nil {
			var zero R
			return zero, fmt.Errorf("failed to build lambda response: %w", err)
		}

		return response, nil
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

// decodeEventBody returns the raw request body bytes for an event body,
// decoding base64 when the event flags it as encoded.
func decodeEventBody(body string, isBase64Encoded bool) ([]byte, error) {
	if body == "" {
		return nil, nil
	}
	if isBase64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return nil, fmt.Errorf("failed to decode base64 body: %w", err)
		}
		return decoded, nil
	}
	return []byte(body), nil
}

// responseBody returns the Lambda response body for a handler response,
// base64-encoding non-text content. It consumes and closes resp.Body. When
// the handler did not set a Content-Type, one is sniffed from the body and
// set on resp.Header to match net/http servers, which apply
// http.DetectContentType on the first Write. Callers must invoke this before
// copying resp.Header into the Lambda response.
func responseBody(resp *http.Response) (body string, isBase64Encoded bool, err error) {
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false, fmt.Errorf("failed to read response body: %w", err)
	}
	if len(bodyBytes) == 0 {
		return "", false, nil
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = http.DetectContentType(bodyBytes)
		resp.Header.Set("Content-Type", contentType)
	}

	if isTextContent(contentType) {
		return string(bodyBytes), false, nil
	}
	return base64.StdEncoding.EncodeToString(bodyBytes), true, nil
}

// headerValue returns the first value for a header key, preferring
// multi-value headers over single-value headers.
func headerValue(single map[string]string, multi map[string][]string, key string) string {
	if len(multi) > 0 {
		for k, vals := range multi {
			if strings.EqualFold(k, key) && len(vals) > 0 {
				return vals[0]
			}
		}
		return ""
	}
	for k, v := range single {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
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
