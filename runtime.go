package voker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"runtime"
	"runtime/debug"
)

const (
	runtimeAPIVersion = "2018-06-01"

	contentTypeJSON = "application/json"

	headerRequestID       = "Lambda-Runtime-Aws-Request-Id"
	headerDeadlineMS      = "Lambda-Runtime-Deadline-Ms"
	headerTraceID         = "Lambda-Runtime-Trace-Id"
	headerCognitoIdentity = "Lambda-Runtime-Cognito-Identity"
	headerClientContext   = "Lambda-Runtime-Client-Context"
	headerFunctionARN     = "Lambda-Runtime-Invoked-Function-Arn"
	headerTenantID        = "Lambda-Runtime-Aws-Tenant-Id"

	headerUserAgent   = "User-Agent"
	headerContentType = "Content-Type"

	headerResponseMode = "Lambda-Runtime-Function-Response-Mode"

	// headerFunctionErrorType carries the error's type both as a request
	// header on Runtime API error endpoint POSTs and as a trailer on failed
	// streaming responses.
	headerFunctionErrorType = "Lambda-Runtime-Function-Error-Type"
	headerStreamErrorBody   = "Lambda-Runtime-Function-Error-Body"
)

var userAgent = buildUserAgent()

// buildUserAgent resolves voker's module version from the binary's build
// info so the User-Agent tracks the released version without a hardcoded
// constant. Builds that replace the module with a local directory (or carry
// no build info) report "devel".
func buildUserAgent() string {
	const modulePath = "github.com/hotsock/voker"
	version := "devel"
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Path == modulePath && info.Main.Version != "" {
			version = info.Main.Version
		} else {
			for _, dep := range info.Deps {
				if dep.Path != modulePath {
					continue
				}
				if dep.Replace != nil {
					if dep.Replace.Version != "" {
						version = dep.Replace.Version
					}
				} else if dep.Version != "" {
					version = dep.Version
				}
				break
			}
		}
	}
	return fmt.Sprintf("voker/%s go/%s", version, runtime.Version())
}

// newRuntimeTransport returns the transport used for Runtime API and
// Extensions API connections. The API is a local endpoint, so requests never
// route through a proxy from HTTP_PROXY et al., and enough idle connections
// are retained for every concurrent worker to keep its connection alive
// between invocations (http.DefaultTransport would keep only two).
func newRuntimeTransport(maxIdleConnsPerHost int) *http.Transport {
	return &http.Transport{
		Proxy:               nil,
		MaxIdleConnsPerHost: maxIdleConnsPerHost,
	}
}

type runtimeClient struct {
	// host is the Runtime API host:port from AWS_LAMBDA_RUNTIME_API.
	host string
	// nextURL is pre-parsed once: GET /next runs on every invocation.
	nextURL      *url.URL
	initErrorURL *url.URL
	httpClient   *http.Client
	logger       *slog.Logger
}

const invocationPathPrefix = "/" + runtimeAPIVersion + "/runtime/invocation/"

func newRuntimeClient(runtimeAPI string, logger *slog.Logger) *runtimeClient {
	return &runtimeClient{
		host:         runtimeAPI,
		nextURL:      &url.URL{Scheme: "http", Host: runtimeAPI, Path: invocationPathPrefix + "next"},
		initErrorURL: &url.URL{Scheme: "http", Host: runtimeAPI, Path: "/" + runtimeAPIVersion + "/runtime/init/error"},
		httpClient: &http.Client{
			Transport: newRuntimeTransport(MaxConcurrency()),
			Timeout:   0, // No timeout for runtime API connections
		},
		logger: logger,
	}
}

// invocationURL builds an invocation-scoped Runtime API URL without a URL
// parse. Request IDs are Lambda-issued identifiers that need no escaping.
func (c *runtimeClient) invocationURL(requestID, suffix string) *url.URL {
	return &url.URL{Scheme: "http", Host: c.host, Path: invocationPathPrefix + requestID + suffix}
}

func (c *runtimeClient) initFailure(errorPayload []byte, errorType string) error {
	return c.post(context.Background(), c.initErrorURL, errorPayload, errorType)
}

type invocation struct {
	requestID string
	payload   []byte
	headers   http.Header
	client    *runtimeClient
}

func (c *runtimeClient) next() (*invocation, error) {
	return c.nextContext(context.Background())
}

func (c *runtimeClient) nextContext(ctx context.Context) (*invocation, error) {
	req := (&http.Request{
		Method: http.MethodGet,
		URL:    c.nextURL,
		Header: http.Header{headerUserAgent: userAgentValue},
	}).WithContext(ctx)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get next invocation: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code from runtime API: %d", resp.StatusCode)
	}

	payload, err := readBody(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to read invocation payload: %w", err)
	}

	return &invocation{
		requestID: resp.Header.Get(headerRequestID),
		payload:   payload,
		headers:   resp.Header,
		client:    c,
	}, nil
}

// userAgentValue is the shared User-Agent header value. Requests only ever
// read it, so it is safe to share across concurrent workers.
var userAgentValue = []string{userAgent}

func readBody(resp *http.Response) ([]byte, error) {
	if resp.ContentLength < 0 {
		return io.ReadAll(resp.Body)
	}

	buf := make([]byte, resp.ContentLength)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

const responsePath = "/response"

func (inv *invocation) success(responsePayload []byte) error {
	url := inv.client.invocationURL(inv.requestID, responsePath)
	return inv.client.post(context.Background(), url, responsePayload, "")
}

func (inv *invocation) successStreaming(ctx context.Context, reader io.Reader, contentType string) (streamErr error, responseErr error) {
	body := &streamingRequestBody{reader: reader}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, inv.client.invocationURL(inv.requestID, responsePath).String(), body)
	if err != nil {
		return nil, err
	}

	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req.Header.Set(headerContentType, contentType)
	req.Header.Set(headerUserAgent, userAgent)
	req.Header.Set(headerResponseMode, "streaming")
	req.TransferEncoding = []string{"chunked"}
	req.Trailer = http.Header{
		headerFunctionErrorType: nil,
		headerStreamErrorBody:   nil,
	}
	body.trailer = req.Trailer
	// Lambda requires the runtime to close the response connection after a
	// streaming invocation instead of returning it to the transport pool.
	req.Close = true

	resp, err := inv.client.httpClient.Do(req)
	if err != nil {
		return body.streamErr, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return body.streamErr, fmt.Errorf("unexpected status code from runtime API: %d", resp.StatusCode)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return body.streamErr, err
	}
	return body.streamErr, nil
}

type streamingRequestBody struct {
	reader     io.Reader
	trailer    http.Header
	streamErr  error
	pendingEOF bool
}

func (b *streamingRequestBody) Read(p []byte) (n int, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			n = 0
			err = io.EOF
			b.setError(newPanicResponse(recovered))
		}
	}()
	if b.pendingEOF {
		return 0, io.EOF
	}

	n, err = b.reader.Read(p)
	if err == nil || err == io.EOF {
		return n, err
	}

	b.setError(err)
	if n > 0 {
		b.pendingEOF = true
		return n, nil
	}
	return 0, io.EOF
}

func (b *streamingRequestBody) Close() error {
	if closer, ok := b.reader.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (b *streamingRequestBody) setError(err error) {
	b.streamErr = err
	errorResponse := newErrorResponse(err)
	errorJSON, marshalErr := json.Marshal(errorResponse)
	if marshalErr != nil {
		errorJSON = fmt.Appendf(nil, `{"errorMessage":"failed to marshal streaming error: %s","errorType":"Runtime.MarshalError"}`, marshalErr)
	}
	b.trailer.Set(headerFunctionErrorType, errorResponse.Type)
	b.trailer.Set(headerStreamErrorBody, base64.StdEncoding.EncodeToString(errorJSON))
}

const errorPath = "/error"

func (inv *invocation) failure(errorPayload []byte, errorType string) error {
	url := inv.client.invocationURL(inv.requestID, errorPath)
	return inv.client.post(context.Background(), url, errorPayload, errorType)
}

// post sends a JSON payload to the Runtime API. errorType, when non-empty,
// is reported in the Lambda-Runtime-Function-Error-Type header on error
// endpoint POSTs.
func (c *runtimeClient) post(ctx context.Context, url *url.URL, body []byte, errorType string) error {
	req := (&http.Request{
		Method: http.MethodPost,
		URL:    url,
		Header: http.Header{
			headerUserAgent:   userAgentValue,
			headerContentType: contentTypeJSONValue,
		},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		// GetBody lets the transport safely retry the request on a stale
		// reused connection, which matters after Lambda thaws the sandbox.
		GetBody: func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		},
	}).WithContext(ctx)
	if errorType != "" {
		req.Header.Set(headerFunctionErrorType, errorType)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to POST to runtime API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("unexpected status code from runtime API: %d", resp.StatusCode)
	}

	_, err = io.Copy(io.Discard, resp.Body)
	if err != nil {
		c.logger.ErrorContext(ctx, "failed to drain response body", "error", err)
	}

	return nil
}

// contentTypeJSONValue is the shared Content-Type header value for Runtime
// API POSTs. Requests only ever read it.
var contentTypeJSONValue = []string{contentTypeJSON}
