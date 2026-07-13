package voker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMaxConcurrency(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int
	}{
		{name: "unset", want: 1},
		{name: "malformed", value: "many", want: 1},
		{name: "zero", value: "0", want: 1},
		{name: "negative", value: "-3", want: 1},
		{name: "configured", value: "8", want: 8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseMaxConcurrency(tt.value))
		})
	}
}

func TestValidateRuntimeConfiguration_RejectsInternalExtensionsOnManagedInstances(t *testing.T) {
	t.Setenv(lambdaEnvInitializationType, managedInstancesInitType)
	called := false
	ext := InternalExtension{
		Name: "unsupported",
		OnInit: func() error {
			called = true
			return nil
		},
	}

	err := validateRuntimeConfiguration(&options{extensions: []InternalExtension{ext}})
	var response *ErrorResponse
	require.ErrorAs(t, err, &response)
	assert.Equal(t, "Runtime.UnsupportedExtension", response.Type)
	assert.Contains(t, response.Message, "Lambda Managed Instances")
	assert.False(t, called, "validation must run before extension initialization")
}

func TestValidateRuntimeConfiguration_AllowsStandardLambdaExtensions(t *testing.T) {
	t.Setenv(lambdaEnvInitializationType, "on-demand")
	err := validateRuntimeConfiguration(&options{extensions: []InternalExtension{{Name: "supported"}}})
	require.NoError(t, err)
}

func TestRuntimeClient_NextContextCancellation(t *testing.T) {
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := newRuntimeClient(server.Listener.Addr().String(), logger)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := client.nextContext(ctx)
		errCh <- err
	}()

	<-requestStarted
	cancel()
	err := <-errCh
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

type concurrentTestEvent struct {
	ID         int   `json:"id"`
	Fail       bool  `json:"fail"`
	DeadlineMS int64 `json:"deadlineMs"`
}

type concurrentTestResponse struct {
	ID        int    `json:"id"`
	RequestID string `json:"requestId"`
	TraceID   string `json:"traceId"`
}

type recordedRuntimeResponse struct {
	kind string
	body []byte
}

func TestRunInvocationWorkers_ConcurrentRoutingAndIsolation(t *testing.T) {
	const (
		invocations = 12
		concurrency = 4
	)

	var next atomic.Int64
	var posted atomic.Int64
	allResponses := make(chan struct{})
	responses := make(map[string]recordedRuntimeResponse, invocations)
	var responsesMu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/2018-06-01/runtime/invocation/next" {
			index := int(next.Add(1) - 1)
			if index >= invocations {
				<-allResponses
				w.WriteHeader(http.StatusGone)
				return
			}
			requestID := fmt.Sprintf("request-%d", index)
			deadlineMS := time.Now().Add(time.Minute + time.Duration(index)*time.Millisecond).UnixMilli()
			w.Header().Set(headerRequestID, requestID)
			w.Header().Set(headerDeadlineMS, strconv.FormatInt(deadlineMS, 10))
			w.Header().Set(headerTraceID, "trace-"+requestID)
			_ = json.NewEncoder(w).Encode(concurrentTestEvent{ID: index, Fail: index%5 == 4, DeadlineMS: deadlineMS})
			return
		}

		prefix := "/2018-06-01/runtime/invocation/"
		path := strings.TrimPrefix(r.URL.Path, prefix)
		parts := strings.Split(path, "/")
		if len(parts) != 2 {
			t.Errorf("unexpected Runtime API path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read response body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		responsesMu.Lock()
		responses[parts[0]] = recordedRuntimeResponse{kind: parts[1], body: body}
		responsesMu.Unlock()
		if posted.Add(1) == invocations {
			close(allResponses)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := newRuntimeClient(server.Listener.Addr().String(), logger)
	opts := &options{logger: logger, maxConcurrency: concurrency}
	var active atomic.Int32
	var peak atomic.Int32
	var firstWave atomic.Int32
	releaseFirstWave := make(chan struct{})

	handler := func(ctx context.Context, event concurrentTestEvent) (concurrentTestResponse, error) {
		activeNow := active.Add(1)
		defer active.Add(-1)
		updatePeak(&peak, activeNow)
		if firstWave.Add(1) <= concurrency {
			if activeNow == concurrency {
				close(releaseFirstWave)
			}
			select {
			case <-releaseFirstWave:
			case <-time.After(time.Second):
				return concurrentTestResponse{}, errors.New("worker pool did not reach configured concurrency")
			}
		}

		lc, ok := FromContext(ctx)
		if !ok {
			return concurrentTestResponse{}, errors.New("missing Lambda context")
		}
		deadline, ok := ctx.Deadline()
		if !ok || deadline.UnixMilli() != event.DeadlineMS {
			return concurrentTestResponse{}, fmt.Errorf("event %d received the wrong deadline", event.ID)
		}
		if event.Fail {
			return concurrentTestResponse{}, &ErrorResponse{
				Type:    "Application.ExpectedFailure",
				Message: fmt.Sprintf("failed event %d", event.ID),
			}
		}
		return concurrentTestResponse{ID: event.ID, RequestID: lc.AwsRequestID, TraceID: lc.TraceID}, nil
	}

	err := runInvocationWorkers(context.Background(), client, opts, func(ctx context.Context, client *runtimeClient, options *options) error {
		return handleInvocationContext(ctx, client, handler, options)
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status code from runtime API: 410")
	assert.Equal(t, int32(concurrency), peak.Load())

	responsesMu.Lock()
	defer responsesMu.Unlock()
	require.Len(t, responses, invocations)
	for i := range invocations {
		requestID := fmt.Sprintf("request-%d", i)
		recorded, ok := responses[requestID]
		require.True(t, ok, "missing response for %s", requestID)
		if i%5 == 4 {
			assert.Equal(t, "error", recorded.kind)
			var response ErrorResponse
			require.NoError(t, json.Unmarshal(recorded.body, &response))
			assert.Equal(t, "Application.ExpectedFailure", response.Type)
			assert.Equal(t, fmt.Sprintf("failed event %d", i), response.Message)
			continue
		}
		assert.Equal(t, "response", recorded.kind)
		var response concurrentTestResponse
		require.NoError(t, json.Unmarshal(recorded.body, &response))
		assert.Equal(t, i, response.ID)
		assert.Equal(t, requestID, response.RequestID)
		assert.Equal(t, "trace-"+requestID, response.TraceID)
	}
}

func TestRunInvocationWorkers_PanicCancelsPendingNextRequests(t *testing.T) {
	const concurrency = 3
	var next atomic.Int32
	var errorResponses atomic.Int32
	pendingCanceled := make(chan struct{}, concurrency-1)
	allNextStarted := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/2018-06-01/runtime/invocation/next":
			requestNumber := next.Add(1)
			if requestNumber == concurrency {
				close(allNextStarted)
			}
			if requestNumber == 1 {
				select {
				case <-allNextStarted:
				case <-time.After(time.Second):
					t.Error("worker pool did not issue all pending /next requests")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.Header().Set(headerRequestID, "panic-request")
				w.Header().Set(headerDeadlineMS, strconv.FormatInt(time.Now().Add(time.Minute).UnixMilli(), 10))
				_, _ = io.WriteString(w, `{}`)
				return
			}
			<-r.Context().Done()
			pendingCanceled <- struct{}{}
		case r.URL.Path == "/2018-06-01/runtime/invocation/panic-request/error":
			errorResponses.Add(1)
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := newRuntimeClient(server.Listener.Addr().String(), logger)
	opts := &options{logger: logger, maxConcurrency: concurrency}
	handler := func(context.Context, concurrentTestEvent) (concurrentTestResponse, error) {
		panic("fatal worker panic")
	}

	err := runInvocationWorkers(context.Background(), client, opts, func(ctx context.Context, client *runtimeClient, options *options) error {
		return handleInvocationContext(ctx, client, handler, options)
	})
	assert.ErrorIs(t, err, errHandlerPanicked)
	assert.Equal(t, int32(1), errorResponses.Load())
	for range concurrency - 1 {
		select {
		case <-pendingCanceled:
		case <-time.After(time.Second):
			t.Fatal("pending /next request was not canceled")
		}
	}
}

func updatePeak(peak *atomic.Int32, value int32) {
	for current := peak.Load(); value > current; current = peak.Load() {
		if peak.CompareAndSwap(current, value) {
			return
		}
	}
}
