package vokerhttp

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
)

// APIGatewayV1 implements [Adapter] for API Gateway v1 REST API
// Lambda proxy integration events.
//
//	vokerhttp.StartHTTP(mux, &vokerhttp.APIGatewayV1{})
type APIGatewayV1 struct{}

// APIGatewayV1Request is the API Gateway v1 REST API proxy integration event.
type APIGatewayV1Request struct {
	Resource                        string                     `json:"resource"`
	Path                            string                     `json:"path"`
	HTTPMethod                      string                     `json:"httpMethod"`
	Headers                         map[string]string          `json:"headers"`
	MultiValueHeaders               map[string][]string        `json:"multiValueHeaders"`
	QueryStringParameters           map[string]string          `json:"queryStringParameters"`
	MultiValueQueryStringParameters map[string][]string        `json:"multiValueQueryStringParameters"`
	PathParameters                  map[string]string          `json:"pathParameters"`
	StageVariables                  map[string]string          `json:"stageVariables"`
	RequestContext                  APIGatewayV1RequestContext `json:"requestContext"`
	Body                            string                     `json:"body"`
	IsBase64Encoded                 bool                       `json:"isBase64Encoded"`
}

// APIGatewayV1RequestContext contains the request context for API Gateway v1 events.
type APIGatewayV1RequestContext struct {
	AccountID         string                      `json:"accountId"`
	APIID             string                      `json:"apiId"`
	Authorizer        map[string]any              `json:"authorizer"`
	DomainName        string                      `json:"domainName"`
	DomainPrefix      string                      `json:"domainPrefix"`
	ExtendedRequestID string                      `json:"extendedRequestId"`
	HTTPMethod        string                      `json:"httpMethod"`
	Identity          APIGatewayV1RequestIdentity `json:"identity"`
	OperationName     string                      `json:"operationName"`
	Path              string                      `json:"path"`
	Protocol          string                      `json:"protocol"`
	RequestID         string                      `json:"requestId"`
	RequestTime       string                      `json:"requestTime"`
	RequestTimeEpoch  int64                       `json:"requestTimeEpoch"`
	ResourceID        string                      `json:"resourceId"`
	ResourcePath      string                      `json:"resourcePath"`
	Stage             string                      `json:"stage"`
}

// APIGatewayV1RequestIdentity contains identity information from API Gateway v1.
type APIGatewayV1RequestIdentity struct {
	AccessKey                     string                 `json:"accessKey"`
	AccountID                     string                 `json:"accountId"`
	APIKey                        string                 `json:"apiKey"`
	APIKeyID                      string                 `json:"apiKeyId"`
	Caller                        string                 `json:"caller"`
	CognitoAuthenticationProvider string                 `json:"cognitoAuthenticationProvider"`
	CognitoAuthenticationType     string                 `json:"cognitoAuthenticationType"`
	CognitoIdentityID             string                 `json:"cognitoIdentityId"`
	CognitoIdentityPoolID         string                 `json:"cognitoIdentityPoolId"`
	PrincipalOrgID                string                 `json:"principalOrgId"`
	SourceIP                      string                 `json:"sourceIp"`
	User                          string                 `json:"user"`
	UserAgent                     string                 `json:"userAgent"`
	UserARN                       string                 `json:"userArn"`
	ClientCert                    APIGatewayV1ClientCert `json:"clientCert"`
}

// APIGatewayV1ClientCert contains TLS client certificate details for API Gateway v1.
type APIGatewayV1ClientCert struct {
	ClientCertPEM string                         `json:"clientCertPem"`
	SubjectDN     string                         `json:"subjectDN"`
	IssuerDN      string                         `json:"issuerDN"`
	SerialNumber  string                         `json:"serialNumber"`
	Validity      APIGatewayV1ClientCertValidity `json:"validity"`
}

// APIGatewayV1ClientCertValidity contains the validity period of a client certificate.
type APIGatewayV1ClientCertValidity struct {
	NotBefore string `json:"notBefore"`
	NotAfter  string `json:"notAfter"`
}

// APIGatewayV1Response is the API Gateway v1 REST API proxy integration response.
type APIGatewayV1Response struct {
	StatusCode        int                 `json:"statusCode"`
	Headers           map[string]string   `json:"headers,omitempty"`
	MultiValueHeaders map[string][]string `json:"multiValueHeaders,omitempty"`
	Body              string              `json:"body"`
	IsBase64Encoded   bool                `json:"isBase64Encoded"`
}

// Request converts an API Gateway v1 event into an *http.Request.
func (a *APIGatewayV1) Request(ctx context.Context, event APIGatewayV1Request) (*http.Request, error) {
	body, err := decodeEventBody(event.Body, event.IsBase64Encoded)
	if err != nil {
		return nil, err
	}

	// Build query string from multi-value parameters (preferred) or single-value
	uri := event.Path
	if params := mergedQueryValues(event.QueryStringParameters, event.MultiValueQueryStringParameters); len(params) > 0 {
		uri += "?" + params.Encode()
	}

	host := headerValue(event.Headers, event.MultiValueHeaders, "host")
	if host == "" {
		host = event.RequestContext.DomainName
	}
	fullURL := "https://" + host + uri

	req, err := http.NewRequestWithContext(ctx, event.HTTPMethod, fullURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	addMergedHeaders(req, event.Headers, event.MultiValueHeaders)

	req.RemoteAddr = event.RequestContext.Identity.SourceIP
	req.RequestURI = uri

	return req, nil
}

// Response converts the handler's *http.Response into an API Gateway v1 response.
func (a *APIGatewayV1) Response(resp *http.Response) (APIGatewayV1Response, error) {
	out := APIGatewayV1Response{
		StatusCode: resp.StatusCode,
	}
	// Encode the body first: responseBody may set a sniffed Content-Type on
	// resp.Header, which must be included in the header map below.
	var err error
	out.Body, out.IsBase64Encoded, err = responseBody(resp)
	if err != nil {
		return APIGatewayV1Response{}, err
	}

	// Use MultiValueHeaders to preserve all header values (including multiple Set-Cookie)
	multiHeaders := make(map[string][]string)
	for k, vals := range resp.Header {
		multiHeaders[strings.ToLower(k)] = vals
	}
	if len(multiHeaders) > 0 {
		out.MultiValueHeaders = multiHeaders
	}

	return out, nil
}
