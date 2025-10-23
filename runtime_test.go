package voker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeClient_Next(t *testing.T) {
	expectedPayload := map[string]string{"key": "value"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/2018-06-01/runtime/invocation/next", r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)

		w.Header().Set(headerRequestID, "test-request-id")
		w.Header().Set(headerDeadlineMS, "1234567890")
		w.Header().Set(headerTraceID, "trace-123")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedPayload)
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)
	inv, err := client.next()

	require.NoError(t, err)
	assert.Equal(t, "test-request-id", inv.requestID)
	assert.Equal(t, "1234567890", inv.headers.Get(headerDeadlineMS))
	assert.Equal(t, "trace-123", inv.headers.Get(headerTraceID))

	var payload map[string]string
	err = json.Unmarshal(inv.payload, &payload)
	require.NoError(t, err)
	assert.Equal(t, expectedPayload, payload)
}

func TestRuntimeClient_Next_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)
	inv, err := client.next()

	assert.Error(t, err)
	assert.Nil(t, inv)
	assert.Contains(t, err.Error(), "unexpected status code")
}

func TestInvocation_Success(t *testing.T) {
	responsePayload := []byte(`{"result":"success"}`)
	responseReceived := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/2018-06-01/runtime/invocation/req-123/response", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, contentTypeJSON, r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, responsePayload, body)

		responseReceived = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)
	inv := &invocation{
		requestID: "req-123",
		client:    client,
	}

	err := inv.success(responsePayload)
	require.NoError(t, err)
	assert.True(t, responseReceived)
}

func TestInvocation_Failure(t *testing.T) {
	errorPayload := []byte(`{"errorMessage":"test error","errorType":"Error"}`)
	errorReceived := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/2018-06-01/runtime/invocation/req-456/error", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, contentTypeJSON, r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, errorPayload, body)

		errorReceived = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)
	inv := &invocation{
		requestID: "req-456",
		client:    client,
	}

	err := inv.failure(errorPayload)
	require.NoError(t, err)
	assert.True(t, errorReceived)
}

func TestRuntimeClient_Post_BadStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)
	err := client.post(context.Background(), client.baseURL+"test/response", []byte("{}"))

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status code")
}
