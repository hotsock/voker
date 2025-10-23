package voker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testEvent struct {
	Name string `json:"name"`
}

type testResponse struct {
	Message string `json:"message"`
}

func TestHandleInvocation_Success(t *testing.T) {
	invocationReceived := false
	responseReceived := false

	// Create a test server simulating the Lambda runtime API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2018-06-01/runtime/invocation/next":
			invocationReceived = true
			w.Header().Set(headerRequestID, "test-request-id")
			w.Header().Set(headerDeadlineMS, "999999999999999")
			w.Header().Set(headerFunctionARN, "arn:aws:lambda:us-east-1:123456789012:function:test")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(testEvent{Name: "test"})

		case "/2018-06-01/runtime/invocation/test-request-id/response":
			responseReceived = true
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()

	// Create runtime client pointing to test server
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger) // Strip "http://"

	handler := func(ctx context.Context, event testEvent) (testResponse, error) {
		assert.Equal(t, "test", event.Name)

		// Verify context has Lambda metadata
		lc, ok := FromContext(ctx)
		assert.True(t, ok)
		assert.Equal(t, "test-request-id", lc.AwsRequestID)
		assert.Equal(t, "arn:aws:lambda:us-east-1:123456789012:function:test", lc.InvokedFunctionArn)

		return testResponse{Message: "hello"}, nil
	}

	err := handleInvocation(client, handler, &options{logger: logger})
	require.NoError(t, err)
	assert.True(t, invocationReceived)
	assert.True(t, responseReceived)
}

func TestHandleInvocation_HandlerError(t *testing.T) {
	errorReceived := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2018-06-01/runtime/invocation/next":
			w.Header().Set(headerRequestID, "test-request-id")
			w.Header().Set(headerDeadlineMS, "999999999999999")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(testEvent{Name: "test"})

		case "/2018-06-01/runtime/invocation/test-request-id/error":
			errorReceived = true
			w.WriteHeader(http.StatusAccepted)

			var errResp ErrorResponse
			err := json.NewDecoder(r.Body).Decode(&errResp)
			require.NoError(t, err)
			assert.Equal(t, "handler error", errResp.Message)
		}
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)

	handler := func(ctx context.Context, event testEvent) (testResponse, error) {
		return testResponse{}, errors.New("handler error")
	}

	err := handleInvocation(client, handler, &options{logger: logger})
	require.NoError(t, err)
	assert.True(t, errorReceived)
}

func TestHandleInvocation_Panic(t *testing.T) {
	panicReceived := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2018-06-01/runtime/invocation/next":
			w.Header().Set(headerRequestID, "test-request-id")
			w.Header().Set(headerDeadlineMS, "999999999999999")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(testEvent{Name: "test"})

		case "/2018-06-01/runtime/invocation/test-request-id/error":
			panicReceived = true
			w.WriteHeader(http.StatusAccepted)

			var errResp ErrorResponse
			err := json.NewDecoder(r.Body).Decode(&errResp)
			require.NoError(t, err)
			assert.Equal(t, "oh no!", errResp.Message)
			assert.NotEmpty(t, errResp.StackTrace)
		}
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)

	handler := func(ctx context.Context, event testEvent) (testResponse, error) {
		panic("oh no!")
	}

	err := handleInvocation(client, handler, &options{logger: logger})
	require.Error(t, err) // Panic should cause fatal error
	assert.True(t, panicReceived)
}

func TestHandleInvocation_InvalidJSON(t *testing.T) {
	errorReceived := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2018-06-01/runtime/invocation/next":
			w.Header().Set(headerRequestID, "test-request-id")
			w.Header().Set(headerDeadlineMS, "999999999999999")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("not json"))

		case "/2018-06-01/runtime/invocation/test-request-id/error":
			errorReceived = true
			w.WriteHeader(http.StatusAccepted)

			var errResp ErrorResponse
			err := json.NewDecoder(r.Body).Decode(&errResp)
			require.NoError(t, err)
			assert.Contains(t, errResp.Message, "failed to unmarshal input")
		}
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)

	handler := func(ctx context.Context, event testEvent) (testResponse, error) {
		return testResponse{Message: "hello"}, nil
	}

	err := handleInvocation(client, handler, &options{logger: logger})
	require.NoError(t, err)
	assert.True(t, errorReceived)
}

func TestHandleInvocation_ContextMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2018-06-01/runtime/invocation/next":
			w.Header().Set(headerRequestID, "req-123")
			w.Header().Set(headerDeadlineMS, "999999999999999")
			w.Header().Set(headerFunctionARN, "arn:aws:lambda:us-west-2:123:function:foo")
			w.Header().Set(headerTraceID, "Root=1-5e9c5b5f-1234567890abcdef")
			w.Header().Set(headerCognitoIdentity, `{"cognito_identity_id":"id-123","cognito_identity_pool_id":"pool-456"}`)
			w.Header().Set(headerClientContext, `{"client":{"installation_id":"install-789"},"custom":{"key":"value"}}`)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(testEvent{Name: "test"})

		case "/2018-06-01/runtime/invocation/req-123/response":
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)

	handler := func(ctx context.Context, event testEvent) (testResponse, error) {
		lc, ok := FromContext(ctx)
		require.True(t, ok)

		assert.Equal(t, "req-123", lc.AwsRequestID)
		assert.Equal(t, "arn:aws:lambda:us-west-2:123:function:foo", lc.InvokedFunctionArn)
		assert.Equal(t, "id-123", lc.Identity.CognitoIdentityID)
		assert.Equal(t, "pool-456", lc.Identity.CognitoIdentityPoolID)
		assert.Equal(t, "install-789", lc.ClientContext.Client.InstallationID)
		assert.Equal(t, "value", lc.ClientContext.Custom["key"])

		// Check deadline
		deadline, ok := ctx.Deadline()
		assert.True(t, ok)
		assert.True(t, deadline.After(time.Now()))

		return testResponse{Message: "ok"}, nil
	}

	err := handleInvocation(client, handler, &options{logger: logger})
	require.NoError(t, err)
}

func TestHandleInvocation_WithXRayTrace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2018-06-01/runtime/invocation/next":
			w.Header().Set(headerRequestID, "req-123")
			w.Header().Set(headerDeadlineMS, "999999999999999")
			w.Header().Set(headerFunctionARN, "arn:aws:lambda:us-west-2:123:function:foo")
			w.Header().Set(headerTraceID, "Root=1-5e9c5b5f-1234567890abcdef")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(testEvent{Name: "test"})

		case "/2018-06-01/runtime/invocation/req-123/response":
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)

	os.Unsetenv("_X_AMZN_TRACE_ID")

	handler := func(ctx context.Context, event testEvent) (testResponse, error) {
		traceID := os.Getenv("_X_AMZN_TRACE_ID")
		assert.Equal(t, "Root=1-5e9c5b5f-1234567890abcdef", traceID)
		return testResponse{Message: "ok"}, nil
	}

	// Test with tracing enabled
	err := handleInvocation(client, handler, &options{enableTraceID: true, logger: logger})
	require.NoError(t, err)

	// Test with tracing disabled - clear the env var first
	os.Unsetenv("_X_AMZN_TRACE_ID")

	handlerNoXRay := func(ctx context.Context, event testEvent) (testResponse, error) {
		traceID := os.Getenv("_X_AMZN_TRACE_ID")
		assert.Equal(t, "", traceID)
		return testResponse{Message: "ok"}, nil
	}

	err = handleInvocation(client, handlerNoXRay, &options{enableTraceID: false, logger: logger})
	require.NoError(t, err)
}

func TestParseDeadline(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantError bool
	}{
		{
			name:      "valid timestamp",
			input:     "1609459200000",
			wantError: false,
		},
		{
			name:      "invalid timestamp",
			input:     "not-a-number",
			wantError: true,
		},
		{
			name:      "empty timestamp",
			input:     "",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deadline, err := parseDeadline(tt.input)
			if tt.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.False(t, deadline.IsZero())
			}
		})
	}
}
