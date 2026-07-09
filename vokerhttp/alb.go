package vokerhttp

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
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
	//
	// When false, the single-value headers format cannot represent repeated
	// response headers, so only the last value of each header is kept —
	// notably, all but the last Set-Cookie are dropped. Handlers that set
	// multiple cookies must use multi-value headers.
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
	body, err := decodeEventBody(event.Body, event.IsBase64Encoded)
	if err != nil {
		return nil, err
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

func buildALBRawQuery(single map[string]string, multi map[string][]string) string {
	// ALB passes URL-encoded query parameter values through without decoding
	// them first, so this preserves event values as-is instead of applying
	// url.Values escaping. The event maps do not preserve the original
	// parameter ordering.
	values := multi
	if len(values) == 0 {
		values = make(map[string][]string, len(single))
		for k, v := range single {
			values[k] = []string{v}
		}
	}

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

// Response converts the handler's *http.Response into an ALB response.
func (a *ALB) Response(resp *http.Response) (ALBResponse, error) {
	out := ALBResponse{
		StatusCode:        resp.StatusCode,
		StatusDescription: fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode)),
	}
	// Encode the body first: responseBody may set a sniffed Content-Type on
	// resp.Header, which must be included in the header maps below.
	var err error
	out.Body, out.IsBase64Encoded, err = responseBody(resp)
	if err != nil {
		return ALBResponse{}, err
	}

	if a.MultiValueHeaders {
		multiHeaders := make(map[string][]string)
		for k, vals := range resp.Header {
			multiHeaders[strings.ToLower(k)] = append([]string(nil), vals...)
		}
		if len(multiHeaders) > 0 {
			out.MultiValueHeaders = multiHeaders
		}
	} else {
		headers := make(map[string]string)
		for k, vals := range resp.Header {
			if len(vals) == 0 {
				continue
			}
			// ALB single-value headers: last value wins for duplicates
			headers[strings.ToLower(k)] = vals[len(vals)-1]
		}
		if len(headers) > 0 {
			out.Headers = headers
		}
	}

	return out, nil
}
