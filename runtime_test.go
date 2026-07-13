package voker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

func TestInvocation_SuccessStreaming(t *testing.T) {
	firstChunkReceived := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/2018-06-01/runtime/invocation/req-stream/response", r.URL.Path)
		assert.Equal(t, "streaming", r.Header.Get(headerResponseMode))
		assert.Equal(t, "text/event-stream", r.Header.Get(headerContentType))
		assert.Equal(t, []string{"chunked"}, r.TransferEncoding)

		first := make([]byte, len("first"))
		_, err := io.ReadFull(r.Body, first)
		require.NoError(t, err)
		assert.Equal(t, "first", string(first))
		close(firstChunkReceived)

		rest, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, "second", string(rest))
		assert.Empty(t, r.Trailer.Get(headerFunctionErrorType))
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)
	inv := &invocation{requestID: "req-stream", client: client}

	reader, writer := io.Pipe()
	producerResult := make(chan error, 1)
	go func() {
		if _, err := io.WriteString(writer, "first"); err != nil {
			producerResult <- err
			return
		}
		<-firstChunkReceived
		_, err := io.WriteString(writer, "second")
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
		producerResult <- err
	}()

	streamErr, err := inv.successStreaming(context.Background(), reader, "text/event-stream")
	require.NoError(t, err)
	require.NoError(t, streamErr)
	require.NoError(t, <-producerResult)
}

type closeTrackingReader struct {
	io.Reader
	closed bool
}

func (r *closeTrackingReader) Close() error {
	r.closed = true
	return nil
}

func TestInvocation_SuccessStreamingClosesReader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	inv := &invocation{
		requestID: "req-stream-close",
		client:    newRuntimeClient(server.Listener.Addr().String(), logger),
	}
	reader := &closeTrackingReader{Reader: bytes.NewBufferString("response")}

	streamErr, err := inv.successStreaming(context.Background(), reader, "")
	require.NoError(t, err)
	require.NoError(t, streamErr)
	assert.True(t, reader.closed)
}

func TestInvocation_StreamingErrorTrailers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, "partial", string(body))
		assert.Equal(t, "HandlerError", r.Trailer.Get(headerFunctionErrorType))

		encoded := r.Trailer.Get(headerStreamErrorBody)
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		require.NoError(t, err)
		var got ErrorResponse
		require.NoError(t, json.Unmarshal(decoded, &got))
		assert.Equal(t, "stream failed", got.Message)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)
	inv := &invocation{requestID: "req-stream-error", client: client}
	wantErr := errors.New("stream failed")

	streamErr, err := inv.successStreaming(context.Background(), &oneShotErrorReader{data: []byte("partial"), err: wantErr}, "")
	require.NoError(t, err)
	assert.ErrorIs(t, streamErr, wantErr)
}

func TestInvocation_StreamingPanicTrailer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		require.NoError(t, err)
		assert.Equal(t, "Runtime.Panic.string", r.Trailer.Get(headerFunctionErrorType))
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)
	inv := &invocation{requestID: "req-stream-panic", client: client}

	streamErr, err := inv.successStreaming(context.Background(), panicReader{}, "")
	require.NoError(t, err)
	var panicErr *ErrorResponse
	require.ErrorAs(t, streamErr, &panicErr)
	assert.Equal(t, "stream panic", panicErr.Message)
	assert.True(t, panicErr.fatal)
}

type oneShotErrorReader struct {
	data []byte
	err  error
}

func (r *oneShotErrorReader) Read(p []byte) (int, error) {
	if r.data == nil {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = nil
	return n, r.err
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) {
	panic("stream panic")
}

func TestInvocation_Failure(t *testing.T) {
	errorPayload := []byte(`{"errorMessage":"test error","errorType":"Error"}`)
	errorReceived := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/2018-06-01/runtime/invocation/req-456/error", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, contentTypeJSON, r.Header.Get("Content-Type"))
		assert.Equal(t, "Application.TestError", r.Header.Get(headerFunctionErrorType))

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

	err := inv.failure(errorPayload, "Application.TestError")
	require.NoError(t, err)
	assert.True(t, errorReceived)
}

func TestRuntimeClient_InitFailure(t *testing.T) {
	errorPayload := []byte(`{"errorMessage":"setup failed","errorType":"Runtime.SetupError"}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/2018-06-01/runtime/init/error", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, contentTypeJSON, r.Header.Get(headerContentType))
		assert.Equal(t, "Runtime.SetupError", r.Header.Get(headerFunctionErrorType))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, errorPayload, body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := newRuntimeClient(server.Listener.Addr().String(), logger)
	require.NoError(t, client.initFailure(errorPayload, "Runtime.SetupError"))
}

func TestRuntimeClient_Post_BadStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)
	err := client.post(context.Background(), client.invocationURL("test", responsePath), []byte("{}"), "")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status code")
}
