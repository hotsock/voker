package vokerhttp

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
)

// FunctionURL implements [Adapter] for Lambda Function URL events
// (payload format 2.0).
//
//	vokerhttp.StartHTTP(mux, &vokerhttp.FunctionURL{})
type FunctionURL struct{}

// Request converts a Function URL event into an *http.Request.
func (a *FunctionURL) Request(ctx context.Context, event FunctionURLRequest) (*http.Request, error) {
	return buildV2Request(ctx, payloadV2Request(event))
}

// Response converts an httptest.ResponseRecorder into a Function URL response.
func (a *FunctionURL) Response(w *httptest.ResponseRecorder) FunctionURLResponse {
	return FunctionURLResponse(buildV2Response(w))
}

// FunctionURLRequest is the Lambda Function URL event (payload format 2.0).
type FunctionURLRequest payloadV2Request

// FunctionURLResponse is the Lambda Function URL response (payload format 2.0).
type FunctionURLResponse payloadV2Response

// payloadV2Request is the shared event shape for payload format 2.0,
// used by both Lambda Function URLs and API Gateway v2 HTTP APIs.
type payloadV2Request struct {
	Version               string                  `json:"version"`
	RouteKey              string                  `json:"routeKey"`
	RawPath               string                  `json:"rawPath"`
	RawQueryString        string                  `json:"rawQueryString"`
	Cookies               []string                `json:"cookies"`
	Headers               map[string]string       `json:"headers"`
	QueryStringParameters map[string]string       `json:"queryStringParameters"`
	PathParameters        map[string]string       `json:"pathParameters"`
	StageVariables        map[string]string       `json:"stageVariables"`
	RequestContext        PayloadV2RequestContext `json:"requestContext"`
	Body                  string                  `json:"body"`
	IsBase64Encoded       bool                    `json:"isBase64Encoded"`
}

// PayloadV2RequestContext contains the request context for payload format 2.0 events.
type PayloadV2RequestContext struct {
	AccountID      string                      `json:"accountId"`
	APIID          string                      `json:"apiId"`
	Authentication PayloadV2Authentication     `json:"authentication"`
	Authorizer     PayloadV2Authorizer         `json:"authorizer"`
	DomainName     string                      `json:"domainName"`
	DomainPrefix   string                      `json:"domainPrefix"`
	HTTP           PayloadV2RequestContextHTTP `json:"http"`
	RequestID      string                      `json:"requestId"`
	RouteKey       string                      `json:"routeKey"`
	Stage          string                      `json:"stage"`
	Time           string                      `json:"time"`
	TimeEpoch      int64                       `json:"timeEpoch"`
}

// PayloadV2RequestContextHTTP contains HTTP-specific fields from the request context.
type PayloadV2RequestContextHTTP struct {
	Method    string `json:"method"`
	Path      string `json:"path"`
	Protocol  string `json:"protocol"`
	SourceIP  string `json:"sourceIp"`
	UserAgent string `json:"userAgent"`
}

// PayloadV2Authorizer contains authorizer information for payload format 2.0.
type PayloadV2Authorizer struct {
	JWT PayloadV2AuthorizerJWT `json:"jwt"`
	IAM PayloadV2AuthorizerIAM `json:"iam"`
}

// PayloadV2AuthorizerJWT contains JWT authorizer claims and scopes.
type PayloadV2AuthorizerJWT struct {
	Claims map[string]string `json:"claims"`
	Scopes []string          `json:"scopes"`
}

// PayloadV2AuthorizerIAM contains IAM authorizer information.
type PayloadV2AuthorizerIAM struct {
	AccessKey       string                   `json:"accessKey"`
	AccountID       string                   `json:"accountId"`
	CallerID        string                   `json:"callerId"`
	CognitoIdentity PayloadV2CognitoIdentity `json:"cognitoIdentity"`
	PrincipalOrgID  string                   `json:"principalOrgId"`
	UserARN         string                   `json:"userArn"`
	UserID          string                   `json:"userId"`
}

// PayloadV2CognitoIdentity contains Cognito identity details for IAM authorizer.
type PayloadV2CognitoIdentity struct {
	AMR            []string `json:"amr"`
	IdentityID     string   `json:"identityId"`
	IdentityPoolID string   `json:"identityPoolId"`
}

// PayloadV2Authentication contains client certificate authentication details.
type PayloadV2Authentication struct {
	ClientCert PayloadV2ClientCert `json:"clientCert"`
}

// PayloadV2ClientCert contains TLS client certificate details.
type PayloadV2ClientCert struct {
	ClientCertPEM string                      `json:"clientCertPem"`
	SubjectDN     string                      `json:"subjectDN"`
	IssuerDN      string                      `json:"issuerDN"`
	SerialNumber  string                      `json:"serialNumber"`
	Validity      PayloadV2ClientCertValidity `json:"validity"`
}

// PayloadV2ClientCertValidity contains the validity period of a client certificate.
type PayloadV2ClientCertValidity struct {
	NotBefore string `json:"notBefore"`
	NotAfter  string `json:"notAfter"`
}

// payloadV2Response is the shared response shape for payload format 2.0.
type payloadV2Response struct {
	StatusCode      int               `json:"statusCode"`
	Headers         map[string]string `json:"headers,omitempty"`
	Body            string            `json:"body"`
	Cookies         []string          `json:"cookies,omitempty"`
	IsBase64Encoded bool              `json:"isBase64Encoded"`
}

func buildV2Request(ctx context.Context, event payloadV2Request) (*http.Request, error) {
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

	uri := event.RawPath
	if event.RawQueryString != "" {
		uri += "?" + event.RawQueryString
	}

	host := headerValue(event.Headers, nil, "host")
	if host == "" {
		host = event.RequestContext.DomainName
	}
	url := "https://" + host + uri

	req, err := http.NewRequestWithContext(ctx, event.RequestContext.HTTP.Method, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Copy headers
	for k, v := range event.Headers {
		req.Header.Set(k, v)
	}

	// Add cookies from the top-level cookies array
	for _, c := range event.Cookies {
		name, value, _ := strings.Cut(c, "=")
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}

	req.RemoteAddr = event.RequestContext.HTTP.SourceIP
	req.RequestURI = uri

	return req, nil
}

func buildV2Response(w *httptest.ResponseRecorder) payloadV2Response {
	resp := payloadV2Response{
		StatusCode: responseStatusCode(w),
	}

	// Flatten headers, separating Set-Cookie into the cookies array
	headers := make(map[string]string)
	for k, vals := range w.Header() {
		lower := strings.ToLower(k)
		if lower == "set-cookie" {
			resp.Cookies = append(resp.Cookies, vals...)
			continue
		}
		headers[lower] = strings.Join(vals, ", ")
	}
	if len(headers) > 0 {
		resp.Headers = headers
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
