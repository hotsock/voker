package vokerhttp

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
)

// ALB implements [Adapter] for Application Load Balancer
// Lambda target group events.
//
//	vokerhttp.StartHTTP(mux, &vokerhttp.ALB{})
type ALB struct {
	// MultiValueHeaders controls whether responses use the ALB
	// multiValueHeaders format. Set this to true when the Lambda target group
	// has the lambda.multi_value_headers.enabled attribute enabled.
	MultiValueHeaders bool
}

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

	uri := event.Path
	if query := buildALBRawQuery(event.QueryStringParameters, event.MultiValueQueryStringParameters); query != "" {
		uri += "?" + query
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

func buildALBRawQuery(single map[string]string, multi map[string][]string) string {
	// ALB passes URL-encoded query parameter values through without decoding
	// them first, so these helpers preserve event values as-is instead of
	// applying url.Values escaping. The event maps do not preserve the original
	// parameter ordering.
	if len(multi) > 0 {
		return encodeALBRawMultiQuery(multi)
	}
	if len(single) > 0 {
		return encodeALBRawSingleQuery(single)
	}
	return ""
}

func encodeALBRawSingleQuery(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(values))
	for _, k := range keys {
		parts = append(parts, k+"="+values[k])
	}
	return strings.Join(parts, "&")
}

func encodeALBRawMultiQuery(values map[string][]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		for _, v := range values[k] {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, "&")
}

// Response converts an httptest.ResponseRecorder into an ALB response.
func (a *ALB) Response(w *httptest.ResponseRecorder) ALBResponse {
	code := responseStatusCode(w)
	resp := ALBResponse{
		StatusCode:        code,
		StatusDescription: fmt.Sprintf("%d %s", code, http.StatusText(code)),
	}

	if a.MultiValueHeaders {
		multiHeaders := make(map[string][]string)
		for k, vals := range w.Header() {
			multiHeaders[strings.ToLower(k)] = append([]string(nil), vals...)
		}
		if len(multiHeaders) > 0 {
			resp.MultiValueHeaders = multiHeaders
		}
	} else {
		headers := make(map[string]string)
		for k, vals := range w.Header() {
			if len(vals) == 0 {
				continue
			}
			// ALB single-value headers: last value wins for duplicates
			headers[strings.ToLower(k)] = vals[len(vals)-1]
		}
		if len(headers) > 0 {
			resp.Headers = headers
		}
	}

	bodyBytes := w.Body.Bytes()
	if len(bodyBytes) == 0 {
		resp.Body = ""
	} else if isTextContent(w.Header().Get("content-type")) {
		resp.Body = string(bodyBytes)
	} else {
		resp.Body = base64.StdEncoding.EncodeToString(bodyBytes)
		resp.IsBase64Encoded = true
	}

	return resp
}
