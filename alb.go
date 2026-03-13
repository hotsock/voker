package voker

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
)

// ALB implements [Adapter] for Application Load Balancer
// Lambda target group events.
//
//	voker.StartHTTP(mux, &voker.ALB{})
type ALB struct{}

// ALBRequest is the ALB Lambda target group event.
type ALBRequest struct {
	RequestContext struct {
		ELB struct {
			TargetGroupArn string `json:"targetGroupArn"`
		} `json:"elb"`
	} `json:"requestContext"`
	HTTPMethod                      string              `json:"httpMethod"`
	Path                            string              `json:"path"`
	QueryStringParameters           map[string]string   `json:"queryStringParameters"`
	MultiValueQueryStringParameters map[string][]string `json:"multiValueQueryStringParameters"`
	Headers                         map[string]string   `json:"headers"`
	MultiValueHeaders               map[string][]string `json:"multiValueHeaders"`
	Body                            string              `json:"body"`
	IsBase64Encoded                 bool                `json:"isBase64Encoded"`
}

// ALBResponse is the ALB Lambda target group response.
type ALBResponse struct {
	StatusCode        int                 `json:"statusCode"`
	StatusDescription string              `json:"statusDescription"`
	Headers           map[string]string   `json:"headers,omitempty"`
	MultiValueHeaders map[string][]string `json:"multiValueHeaders,omitempty"`
	Body              string              `json:"body"`
	IsBase64Encoded   bool                `json:"isBase64Encoded"`
}

// Request converts an ALB event into an *http.Request.
func (a *ALB) Request(ctx context.Context, event ALBRequest) (*http.Request, error) {
	var body []byte
	if event.Body != "" {
		if event.IsBase64Encoded {
			var err error
			body, err = base64.StdEncoding.DecodeString(event.Body)
			if err != nil {
				return nil, fmt.Errorf("failed to decode base64 body: %w", err)
			}
		} else {
			body = []byte(event.Body)
		}
	}

	// Build URL from path and query string parameters.
	// Prefer multi-value query string parameters over single-value.
	uri := event.Path
	if len(event.MultiValueQueryStringParameters) > 0 {
		params := url.Values{}
		for k, vals := range event.MultiValueQueryStringParameters {
			for _, v := range vals {
				params.Add(k, v)
			}
		}
		uri += "?" + params.Encode()
	} else if len(event.QueryStringParameters) > 0 {
		params := url.Values{}
		for k, v := range event.QueryStringParameters {
			params.Set(k, v)
		}
		uri += "?" + params.Encode()
	}

	// Resolve host and scheme from headers (prefer multi-value if available)
	host := headerValue(event.Headers, event.MultiValueHeaders, "host")
	scheme := "https"
	if proto := headerValue(event.Headers, event.MultiValueHeaders, "x-forwarded-proto"); proto != "" {
		scheme = proto
	}
	fullURL := scheme + "://" + host + uri

	req, err := http.NewRequestWithContext(ctx, event.HTTPMethod, fullURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Prefer multi-value headers over single-value
	if len(event.MultiValueHeaders) > 0 {
		for k, vals := range event.MultiValueHeaders {
			for _, v := range vals {
				req.Header.Add(k, v)
			}
		}
	} else {
		for k, v := range event.Headers {
			req.Header.Set(k, v)
		}
	}

	xff := headerValue(event.Headers, event.MultiValueHeaders, "x-forwarded-for")
	if xff != "" {
		// x-forwarded-for may contain multiple IPs; the first is the client
		ip, _, _ := strings.Cut(xff, ",")
		req.RemoteAddr = strings.TrimSpace(ip)
	}

	req.RequestURI = uri

	return req, nil
}

// headerValue returns the first value for a header key, preferring
// multi-value headers over single-value headers.
func headerValue(single map[string]string, multi map[string][]string, key string) string {
	if len(multi) > 0 {
		if vals := multi[key]; len(vals) > 0 {
			return vals[0]
		}
		return ""
	}
	return single[key]
}

// Response converts an httptest.ResponseRecorder into an ALB response.
func (a *ALB) Response(w *httptest.ResponseRecorder) ALBResponse {
	resp := ALBResponse{
		StatusCode:        w.Code,
		StatusDescription: fmt.Sprintf("%d %s", w.Code, http.StatusText(w.Code)),
	}

	headers := make(map[string]string)
	for k, vals := range w.Header() {
		// ALB single-value headers: last value wins for duplicates
		headers[strings.ToLower(k)] = vals[len(vals)-1]
	}
	if len(headers) > 0 {
		resp.Headers = headers
	}

	bodyBytes := w.Body.Bytes()
	if isTextContent(w.Header().Get("content-type")) {
		resp.Body = string(bodyBytes)
	} else {
		resp.Body = base64.StdEncoding.EncodeToString(bodyBytes)
		resp.IsBase64Encoded = true
	}

	return resp
}
