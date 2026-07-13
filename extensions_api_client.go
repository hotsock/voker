package voker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	headerExtensionName       = "lambda-extension-name"
	headerExtensionIdentifier = "lambda-extension-identifier"
	extensionAPIVersion       = "2020-01-01"
)

// ExtensionEventType identifies the kind of event delivered to an extension
// by the Lambda Extensions API.
type ExtensionEventType string

// ExtensionEventInvoke is delivered for each function invocation. It is the
// only event type available to internal extensions: Lambda sends SHUTDOWN
// events exclusively to external extensions, so voker exposes shutdown via
// [InternalExtension.OnSIGTERM] instead.
const ExtensionEventInvoke ExtensionEventType = "INVOKE"

type extensionAPIClient struct {
	baseURL     string
	registerURL string
	nextURL     string
	httpClient  *http.Client
}

// newExtensionAPIClient returns a client for the Extensions API.
// maxIdleConnsPerHost should cover one long-poll connection per registered
// extension.
func newExtensionAPIClient(address string, maxIdleConnsPerHost int) *extensionAPIClient {
	client := &http.Client{
		Transport: newRuntimeTransport(max(maxIdleConnsPerHost, 1)),
		Timeout:   0, // no timeout for Extensions API
	}

	baseURL := "http://" + address + "/" + extensionAPIVersion + "/extension/"
	return &extensionAPIClient{
		baseURL:     baseURL,
		registerURL: baseURL + "register",
		nextURL:     baseURL + "event/next",
		httpClient:  client,
	}
}

type registerRequest struct {
	Events []ExtensionEventType `json:"events"`
}

func (c *extensionAPIClient) register(name string, events []ExtensionEventType) (string, error) {
	body, err := json.Marshal(registerRequest{Events: events})
	if err != nil {
		return "", fmt.Errorf("failed to marshal register request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.registerURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to create register request: %w", err)
	}
	req.Header.Set(headerExtensionName, name)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to register extension: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("register failed with status: %d", resp.StatusCode)
	}

	return resp.Header.Get(headerExtensionIdentifier), nil
}

// ExtensionEventPayload is the event delivered to an extension's event loop
// by the Lambda Extensions API and passed to [InternalExtension.OnInvoke].
type ExtensionEventPayload struct {
	EventType          ExtensionEventType `json:"eventType"`
	DeadlineMs         int64              `json:"deadlineMs"`
	ShutdownReason     string             `json:"shutdownReason,omitempty"`
	RequestID          string             `json:"requestId,omitempty"`
	InvokedFunctionArn string             `json:"invokedFunctionArn,omitempty"`
	Tracing            ExtensionTracing   `json:"tracing"`
}

// ExtensionTracing carries the X-Ray tracing header delivered with an
// extension event.
type ExtensionTracing struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// next waits for the next extension event
func (c *extensionAPIClient) next(id string) (*ExtensionEventPayload, error) {
	req, err := http.NewRequest(http.MethodGet, c.nextURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create next request: %w", err)
	}
	req.Header.Set(headerExtensionIdentifier, id)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get next event: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("next failed with status: %d", resp.StatusCode)
	}

	var payload ExtensionEventPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("failed to decode event: %w", err)
	}

	return &payload, nil
}
