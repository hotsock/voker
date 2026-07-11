// Package vokercfn provides a type-safe entrypoint for AWS CloudFormation
// custom resource Lambda functions.
//
// CloudFormation expects custom resources to report their result with an HTTP
// PUT to the presigned ResponseURL in the invocation event. [Start] and [Wrap]
// handle that protocol, including failure responses and physical resource ID
// fallbacks, so handlers can focus on provisioning logic.
package vokercfn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"time"
	"unicode/utf8"

	"github.com/hotsock/voker"
)

// RequestType identifies a CloudFormation custom resource operation.
type RequestType string

const (
	RequestCreate RequestType = "Create"
	RequestUpdate RequestType = "Update"
	RequestDelete RequestType = "Delete"
)

// Event is a CloudFormation custom resource request. P is the handler's
// type-safe representation of ResourceProperties.
type Event[P any] struct {
	RequestType           RequestType `json:"RequestType"`
	RequestID             string      `json:"RequestId"`
	ResponseURL           string      `json:"ResponseURL"`
	ResourceType          string      `json:"ResourceType"`
	PhysicalResourceID    string      `json:"PhysicalResourceId,omitempty"`
	LogicalResourceID     string      `json:"LogicalResourceId"`
	StackID               string      `json:"StackId"`
	ResourceProperties    P           `json:"ResourceProperties"`
	OldResourceProperties P           `json:"OldResourceProperties,omitempty"`
}

// Result describes a successful custom resource operation.
//
// PhysicalResourceID should be stable for the lifetime of a resource. If it
// is empty, Voker uses the request ID for Create and the existing physical ID
// for Update and Delete. Returning a new ID from an Update tells
// CloudFormation that the resource was replaced.
//
// Data is exposed through Fn::GetAtt. Set NoEcho to mask Data values in the
// CloudFormation console and API responses.
type Result[D any] struct {
	PhysicalResourceID string
	Data               D
	NoEcho             bool
}

// Handler is the signature accepted by [Start] and [Wrap]. A handler may
// return a Result together with an error; its PhysicalResourceID is retained
// in the FAILED response, while Data and NoEcho are ignored.
type Handler[P, D any] func(context.Context, Event[P]) (Result[D], error)

// Start starts the Lambda runtime loop for a CloudFormation custom resource.
// Handler errors are reported to CloudFormation as FAILED responses and do not
// become Lambda invocation errors. Failure to deliver the response does become
// a Lambda invocation error so it is visible in Lambda monitoring.
//
// Panics are reported to CloudFormation on a best-effort basis and then
// re-panicked so Voker preserves its normal panic logging and runtime behavior.
func Start[P, D any](handler Handler[P, D], opts ...voker.Option) {
	voker.Start(Wrap(handler), opts...)
}

// Wrap adapts a CloudFormation custom resource Handler for [voker.Start]. Most
// programs should call [Start] directly; Wrap is useful when composing a
// custom entrypoint. It accepts the raw invocation so CloudFormation protocol
// metadata can be decoded before typed properties. If property decoding fails,
// Wrap can therefore still send the required FAILED response.
func Wrap[P, D any](handler Handler[P, D]) func(context.Context, json.RawMessage) (struct{}, error) {
	return wrapRawWithClient(handler, http.DefaultClient)
}

type httpClient interface {
	Do(*http.Request) (*http.Response, error)
}

type status string

const (
	statusSuccess status = "SUCCESS"
	statusFailed  status = "FAILED"

	maxResponseBytes  = 4096
	maxErrorBodyBytes = 8192
	responseAttempts  = 3
)

var responseRetryDelays = [...]time.Duration{100 * time.Millisecond, 250 * time.Millisecond}

type response struct {
	Status             status `json:"Status"`
	Reason             string `json:"Reason,omitempty"`
	PhysicalResourceID string `json:"PhysicalResourceId"`
	StackID            string `json:"StackId"`
	RequestID          string `json:"RequestId"`
	LogicalResourceID  string `json:"LogicalResourceId"`
	NoEcho             bool   `json:"NoEcho,omitempty"`
	Data               any    `json:"Data,omitempty"`
}

type eventMetadata struct {
	RequestType        RequestType `json:"RequestType"`
	RequestID          string      `json:"RequestId"`
	ResponseURL        string      `json:"ResponseURL"`
	PhysicalResourceID string      `json:"PhysicalResourceId,omitempty"`
	LogicalResourceID  string      `json:"LogicalResourceId"`
	StackID            string      `json:"StackId"`
}

func wrapRawWithClient[P, D any](handler Handler[P, D], client httpClient) func(context.Context, json.RawMessage) (struct{}, error) {
	return func(ctx context.Context, payload json.RawMessage) (struct{}, error) {
		var metadata eventMetadata
		if err := json.Unmarshal(payload, &metadata); err != nil {
			return struct{}{}, fmt.Errorf("decode CloudFormation event metadata: %w", err)
		}

		var event Event[P]
		if err := json.Unmarshal(payload, &event); err != nil {
			if metadata.ResponseURL == "" {
				return struct{}{}, fmt.Errorf("decode CloudFormation event: %w", err)
			}
			failed := response{
				Status:             statusFailed,
				Reason:             fmt.Sprintf("failed to decode CloudFormation event: %v", err),
				PhysicalResourceID: fallbackMetadataPhysicalResourceID(metadata),
				StackID:            metadata.StackID,
				RequestID:          metadata.RequestID,
				LogicalResourceID:  metadata.LogicalResourceID,
			}
			if sendErr := sendResponse(ctx, metadata.ResponseURL, failed, client); sendErr != nil {
				return struct{}{}, fmt.Errorf("send CloudFormation decode failure: %w", sendErr)
			}
			return struct{}{}, nil
		}

		return wrapWithClient(handler, client)(ctx, event)
	}
}

func wrapWithClient[P, D any](handler Handler[P, D], client httpClient) func(context.Context, Event[P]) (struct{}, error) {
	return func(ctx context.Context, event Event[P]) (struct{}, error) {
		base := response{
			PhysicalResourceID: fallbackPhysicalResourceID(event),
			StackID:            event.StackID,
			RequestID:          event.RequestID,
			LogicalResourceID:  event.LogicalResourceID,
		}

		defer func() {
			if recovered := recover(); recovered != nil {
				failed := base
				failed.Status = statusFailed
				failed.Reason = "handler panicked; see Lambda logs for details"
				// Preserve the original panic. Voker will record its value and stack;
				// the CloudFormation response is necessarily best effort here.
				_ = sendResponse(ctx, event.ResponseURL, failed, client)
				panic(recovered)
			}
		}()

		result, handlerErr := handler(ctx, event)
		resp := base
		if result.PhysicalResourceID != "" {
			resp.PhysicalResourceID = result.PhysicalResourceID
		}

		if handlerErr != nil {
			resp.Status = statusFailed
			resp.Reason = handlerErr.Error()
			if resp.Reason == "" {
				resp.Reason = "handler returned an error"
			}
		} else {
			resp.Status = statusSuccess
			resp.NoEcho = result.NoEcho
			if !isNil(result.Data) {
				resp.Data = result.Data
			}
		}

		if err := sendResponse(ctx, event.ResponseURL, resp, client); err != nil {
			return struct{}{}, fmt.Errorf("send CloudFormation response: %w", err)
		}
		return struct{}{}, nil
	}
}

func fallbackPhysicalResourceID[P any](event Event[P]) string {
	if event.RequestType != RequestCreate && event.PhysicalResourceID != "" {
		return event.PhysicalResourceID
	}
	return event.RequestID
}

func fallbackMetadataPhysicalResourceID(event eventMetadata) string {
	if event.RequestType != RequestCreate && event.PhysicalResourceID != "" {
		return event.PhysicalResourceID
	}
	return event.RequestID
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	value := reflect.ValueOf(v)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func sendResponse(ctx context.Context, responseURL string, resp response, client httpClient) error {
	body := marshalResponse(resp)
	var lastErr error
	for attempt := range responseAttempts {
		if attempt > 0 {
			timer := time.NewTimer(responseRetryDelays[attempt-1])
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}

		err, retry := putResponse(ctx, responseURL, body, client)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry {
			return err
		}
	}
	return fmt.Errorf("PUT response failed after %d attempts: %w", responseAttempts, lastErr)
}

func putResponse(ctx context.Context, responseURL string, body []byte, client httpClient) (err error, retry bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, responseURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create PUT request: %w", err), false
	}
	// A presigned CloudFormation response URL expects no Content-Type header.
	req.Header.Del("Content-Type")

	httpResp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("PUT response: %w", err), ctx.Err() == nil
	}
	if httpResp.Body == nil {
		httpResp.Body = http.NoBody
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(httpResp.Body, maxErrorBodyBytes))
	closeErr := httpResp.Body.Close()
	if readErr != nil {
		return fmt.Errorf("read PUT response: %w", readErr), true
	}
	if closeErr != nil {
		return fmt.Errorf("close PUT response: %w", closeErr), true
	}
	if httpResp.StatusCode != http.StatusOK {
		retry := httpResp.StatusCode == http.StatusRequestTimeout ||
			httpResp.StatusCode == http.StatusTooManyRequests ||
			httpResp.StatusCode >= http.StatusInternalServerError
		if len(responseBody) == 0 {
			return fmt.Errorf("PUT response returned %s", httpResp.Status), retry
		}
		return fmt.Errorf("PUT response returned %s: %s", httpResp.Status, responseBody), retry
	}
	return nil, false
}

// marshalResponse always returns a valid response no larger than the
// CloudFormation protocol's 4096-byte limit. If success data cannot be encoded
// or is too large, CloudFormation receives a compact FAILED response instead
// of waiting for the custom resource to time out.
func marshalResponse(resp response) []byte {
	body, err := json.Marshal(resp)
	if err == nil && len(body) <= maxResponseBytes {
		return body
	}

	failed := resp
	failed.Status = statusFailed
	failed.NoEcho = false
	failed.Data = nil
	if err != nil {
		failed.Reason = fmt.Sprintf("failed to encode CloudFormation response: %v", err)
	} else {
		failed.Reason = "CloudFormation response exceeds the 4096-byte limit"
	}
	return marshalFailedResponse(failed)
}

func marshalFailedResponse(resp response) []byte {
	resp.Status = statusFailed
	resp.NoEcho = false
	resp.Data = nil

	for {
		body, err := json.Marshal(resp)
		if err == nil && len(body) <= maxResponseBytes {
			return body
		}
		if resp.Reason == "" {
			// All remaining fields are protocol strings and cannot fail to marshal.
			return []byte(`{"Status":"FAILED","Reason":"response metadata exceeds the 4096-byte limit"}`)
		}
		_, size := utf8.DecodeLastRuneInString(resp.Reason)
		resp.Reason = resp.Reason[:len(resp.Reason)-size]
	}
}
