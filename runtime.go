package voker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"
)

const (
	vokerVersion = "0.9.0"

	runtimeAPIVersion = "2018-06-01"

	contentTypeJSON = "application/json"

	headerRequestID       = "lambda-runtime-aws-request-id"
	headerDeadlineMS      = "lambda-runtime-deadline-ms"
	headerTraceID         = "lambda-runtime-trace-id"
	headerCognitoIdentity = "lambda-runtime-cognito-identity"
	headerClientContext   = "lambda-runtime-client-context"
	headerFunctionARN     = "lambda-runtime-invoked-function-arn"

	headerUserAgent   = "user-agent"
	headerContentType = "content-type"
)

var userAgent = fmt.Sprintf("voker/%s go/%s", vokerVersion, runtime.Version())

type runtimeClient struct {
	baseURL    string
	nextURL    string
	httpClient *http.Client
	logger     *slog.Logger
}

func newRuntimeClient(runtimeAPI string, logger *slog.Logger) *runtimeClient {
	baseURL := fmt.Sprintf("http://%s/%s/runtime/invocation/", runtimeAPI, runtimeAPIVersion)

	return &runtimeClient{
		baseURL: baseURL,
		nextURL: baseURL + "next",
		httpClient: &http.Client{
			Timeout: 0, // No timeout for runtime API connections
		},
		logger: logger,
	}
}

type invocation struct {
	requestID string
	payload   []byte
	headers   http.Header
	client    *runtimeClient
}

func (c *runtimeClient) next() (*invocation, error) {
	req, err := http.NewRequest(http.MethodGet, c.nextURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set(headerUserAgent, userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get next invocation: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code from runtime API: %d", resp.StatusCode)
	}

	payload, err := io.ReadAll(resp.Body)
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

const responsePath = "/response"

func (inv *invocation) success(responsePayload []byte) error {
	url := inv.client.baseURL + inv.requestID + responsePath
	return inv.client.post(context.Background(), url, responsePayload)
}

const errorPath = "/error"

func (inv *invocation) failure(errorPayload []byte) error {
	url := inv.client.baseURL + inv.requestID + errorPath
	return inv.client.post(context.Background(), url, errorPayload)
}

func (c *runtimeClient) post(ctx context.Context, url string, body []byte) error {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create POST request: %w", err)
	}

	req.Header.Set(headerContentType, contentTypeJSON)
	req.Header.Set(headerUserAgent, userAgent)

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
