package vokerhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type writeRecorder struct {
	bytes.Buffer
	writes [][]byte
}

func (w *writeRecorder) Write(p []byte) (int, error) {
	w.writes = append(w.writes, append([]byte(nil), p...))
	return w.Buffer.Write(p)
}

func decodeStreamingResponse(t *testing.T, data []byte) (StreamingResponseMetadata, []byte) {
	t.Helper()

	delimiter := bytes.Repeat([]byte{0}, len(streamingMetadataDelimiter))
	parts := bytes.SplitN(data, delimiter, 2)
	require.Len(t, parts, 2)
	var metadata StreamingResponseMetadata
	require.NoError(t, json.Unmarshal(parts[0], &metadata))
	return metadata, parts[1]
}

func TestStreamingEventHandler_FunctionURL(t *testing.T) {
	event := newTestFunctionURLRequest()
	var sawEvent bool
	handler := streamingEventHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawEvent = EventFromContext[FunctionURLRequest](r.Context())
		flusher, ok := w.(http.Flusher)
		assert.True(t, ok)
		if !ok {
			return
		}

		w.Header().Add("X-Value", "one")
		w.Header().Add("X-Value", "two")
		http.SetCookie(w, &http.Cookie{Name: "a", Value: "one"})
		http.SetCookie(w, &http.Cookie{Name: "b", Value: "two"})
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "first")
		flusher.Flush()
		_, _ = io.WriteString(w, "second")
	}), &FunctionURL{})

	response, err := handler(context.Background(), event)
	require.NoError(t, err)
	typedResponse, ok := response.(interface{ ContentType() string })
	require.True(t, ok)
	assert.Equal(t, streamingIntegrationContentType, typedResponse.ContentType())
	data, err := io.ReadAll(response)
	require.NoError(t, err)
	require.True(t, sawEvent)
	metadata, body := decodeStreamingResponse(t, data)
	assert.Equal(t, http.StatusCreated, metadata.StatusCode)
	assert.Equal(t, "text/event-stream", metadata.Headers["content-type"])
	assert.Equal(t, "one, two", metadata.Headers["x-value"])
	assert.Equal(t, []string{"a=one", "b=two"}, metadata.Cookies)
	assert.Equal(t, "firstsecond", string(body))
}

func TestStreamingEventHandler_APIGatewayV1(t *testing.T) {
	handler := streamingEventHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("X-Value", "one")
		w.Header().Add("X-Value", "two")
		http.SetCookie(w, &http.Cookie{Name: "a", Value: "one"})
		w.WriteHeader(http.StatusAccepted)
	}), &APIGatewayV1{})

	response, err := handler(context.Background(), newTestAPIGatewayV1Request())
	require.NoError(t, err)
	data, err := io.ReadAll(response)
	require.NoError(t, err)

	metadata, body := decodeStreamingResponse(t, data)
	assert.Equal(t, http.StatusAccepted, metadata.StatusCode)
	assert.Equal(t, []string{"one", "two"}, metadata.MultiValueHeaders["x-value"])
	assert.Equal(t, []string{"a=one"}, metadata.MultiValueHeaders["set-cookie"])
	assert.Empty(t, body)
}

func TestStreamingResponseWriter_PreservesWrites(t *testing.T) {
	destination := &writeRecorder{}
	w := newStreamingResponseWriter(destination, (&FunctionURL{}).StreamingResponseMetadata)
	w.Header().Set("Content-Type", "text/event-stream")

	_, err := io.WriteString(w, "first")
	require.NoError(t, err)
	w.Flush()
	_, err = io.WriteString(w, "second")
	require.NoError(t, err)
	require.NoError(t, w.finish())

	require.GreaterOrEqual(t, len(destination.writes), 4)
	assert.Equal(t, "first", string(destination.writes[len(destination.writes)-2]))
	assert.Equal(t, "second", string(destination.writes[len(destination.writes)-1]))
}

func TestStreamingResponseWriter_SniffsContentType(t *testing.T) {
	destination := &bytes.Buffer{}
	w := newStreamingResponseWriter(destination, (&FunctionURL{}).StreamingResponseMetadata)

	_, err := io.WriteString(w, "plain text")
	require.NoError(t, err)
	require.NoError(t, w.finish())

	metadata, body := decodeStreamingResponse(t, destination.Bytes())
	assert.Equal(t, "text/plain; charset=utf-8", metadata.Headers["content-type"])
	assert.Equal(t, "plain text", string(body))
}

func TestStreamingEventHandler_RequestError(t *testing.T) {
	handler := streamingEventHandler(http.NewServeMux(), streamingErrAdapter{})

	_, err := handler(context.Background(), struct{}{})
	require.Error(t, err)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestStreamingEventHandler_PanicBeforeCommit(t *testing.T) {
	handler := streamingEventHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("before commit")
	}), &FunctionURL{})

	_, err := handler(context.Background(), newTestFunctionURLRequest())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "before commit")
}

func TestStreamingEventHandler_ErrorAfterCommit(t *testing.T) {
	handler := streamingEventHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "partial")
		panic("after commit")
	}), &FunctionURL{})

	response, err := handler(context.Background(), newTestFunctionURLRequest())
	require.NoError(t, err)
	body, err := io.ReadAll(response)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "after commit")
	metadata, partial := decodeStreamingResponse(t, body)
	assert.Equal(t, http.StatusOK, metadata.StatusCode)
	assert.Equal(t, "partial", string(partial))
}

type streamingErrAdapter struct{}

func (streamingErrAdapter) Request(context.Context, struct{}) (*http.Request, error) {
	return nil, io.ErrUnexpectedEOF
}

func (streamingErrAdapter) StreamingResponseMetadata(int, http.Header) StreamingResponseMetadata {
	return StreamingResponseMetadata{}
}

var (
	_ StreamingAdapter[FunctionURLRequest]  = (*FunctionURL)(nil)
	_ StreamingAdapter[APIGatewayV1Request] = (*APIGatewayV1)(nil)
	_ http.ResponseWriter                   = (*streamingResponseWriter)(nil)
	_ http.Flusher                          = (*streamingResponseWriter)(nil)
	_ interface{ FlushError() error }       = (*streamingResponseWriter)(nil)
)
