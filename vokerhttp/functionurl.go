package vokerhttp

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
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

// Response converts the handler's *http.Response into a Function URL response.
func (a *FunctionURL) Response(resp *http.Response) (FunctionURLResponse, error) {
	out, err := buildV2Response(resp)
	return FunctionURLResponse(out), err
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
	body, err := decodeEventBody(event.Body, event.IsBase64Encoded)
	if err != nil {
		return nil, err
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

	// Restore the Cookie header from the top-level cookies array verbatim.
	// req.AddCookie is deliberately avoided: it sanitizes values (quoting
	// values containing spaces or commas, dropping invalid octets), which
	// would alter what the client actually sent.
	if len(event.Cookies) > 0 {
		req.Header.Set("Cookie", strings.Join(event.Cookies, "; "))
	}

	req.RemoteAddr = event.RequestContext.HTTP.SourceIP
	// AWS can deliver decoded characters in rawPath (for example, a literal
	// space from HTTP API v2). Derive RequestURI from the parsed URL so the
	// request target has valid HTTP escaping while retaining RawQuery.
	req.RequestURI = req.URL.RequestURI()

	return req, nil
}

func buildV2Response(resp *http.Response) (payloadV2Response, error) {
	out := payloadV2Response{
		StatusCode: resp.StatusCode,
	}
	// Encode the body first: responseBody may set a sniffed Content-Type on
	// resp.Header, which must be included in the header map below.
	var err error
	out.Body, out.IsBase64Encoded, err = responseBody(resp)
	if err != nil {
		return payloadV2Response{}, err
	}

	// Flatten headers, separating Set-Cookie into the cookies array
	headers := make(map[string]string)
	for k, vals := range resp.Header {
		lower := strings.ToLower(k)
		if lower == "set-cookie" {
			out.Cookies = append(out.Cookies, vals...)
			continue
		}
		headers[lower] = strings.Join(vals, ", ")
	}
	if len(headers) > 0 {
		out.Headers = headers
	}

	return out, nil
}
