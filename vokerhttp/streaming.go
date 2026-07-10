package vokerhttp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const streamingIntegrationContentType = "application/vnd.awslambda.http-integration-response"

var streamingMetadataDelimiter = [8]byte{}

type streamingHTTPResponse struct {
	*io.PipeReader
}

func (streamingHTTPResponse) ContentType() string {
	return streamingIntegrationContentType
}

// StreamingResponseMetadata is the HTTP response prelude Lambda expects before
// a streaming response body.
type StreamingResponseMetadata struct {
	StatusCode        int                 `json:"statusCode,omitempty"`
	Headers           map[string]string   `json:"headers,omitempty"`
	MultiValueHeaders map[string][]string `json:"multiValueHeaders,omitempty"`
	Cookies           []string            `json:"cookies,omitempty"`
}

type streamingResponseWriter struct {
	destination io.Writer
	header      http.Header
	metadata    func(int, http.Header) StreamingResponseMetadata
	statusCode  int
	committed   bool
	ready       chan struct{}
	err         error
}

func newStreamingResponseWriter(destination io.Writer, metadata func(int, http.Header) StreamingResponseMetadata) *streamingResponseWriter {
	return &streamingResponseWriter{
		destination: destination,
		header:      make(http.Header),
		metadata:    metadata,
		ready:       make(chan struct{}),
	}
}

func (w *streamingResponseWriter) Header() http.Header {
	return w.header
}

func (w *streamingResponseWriter) WriteHeader(statusCode int) {
	if w.committed || w.err != nil {
		return
	}
	if statusCode < 100 || statusCode > 999 {
		panic(fmt.Sprintf("invalid WriteHeader code %v", statusCode))
	}
	// Lambda's integration metadata has one final status code and cannot carry
	// informational responses.
	if statusCode >= 100 && statusCode <= 199 {
		return
	}
	w.commit(statusCode)
}

func (w *streamingResponseWriter) Write(p []byte) (int, error) {
	if !w.committed && w.err == nil {
		if len(p) > 0 && w.header.Get("Content-Type") == "" {
			w.header.Set("Content-Type", http.DetectContentType(p))
		}
		w.commit(http.StatusOK)
	}
	if w.err != nil {
		return 0, w.err
	}
	return w.destination.Write(p)
}

func (w *streamingResponseWriter) Flush() {
	_ = w.FlushError()
}

func (w *streamingResponseWriter) FlushError() error {
	if !w.committed && w.err == nil {
		w.commit(http.StatusOK)
	}
	return w.err
}

func (w *streamingResponseWriter) finish() error {
	if !w.committed && w.err == nil {
		w.commit(http.StatusOK)
	}
	return w.err
}

func (w *streamingResponseWriter) commit(statusCode int) {
	w.statusCode = statusCode
	w.committed = true
	close(w.ready)

	metadata, err := json.Marshal(w.metadata(statusCode, w.header))
	if err != nil {
		w.err = fmt.Errorf("failed to marshal streaming response metadata: %w", err)
		return
	}
	if _, err := w.destination.Write(metadata); err != nil {
		w.err = err
		return
	}
	if _, err := w.destination.Write(streamingMetadataDelimiter[:]); err != nil {
		w.err = err
	}
}
