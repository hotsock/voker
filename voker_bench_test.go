package voker

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// BenchmarkHandleInvocation_HotPath measures the full invocation cycle
// including network I/O, JSON marshaling, and context operations.
func BenchmarkHandleInvocation_HotPath(b *testing.B) {
	// Create a test server simulating the Lambda runtime API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2018-06-01/runtime/invocation/next":
			w.Header().Set(headerRequestID, "bench-request-id")
			w.Header().Set(headerDeadlineMS, "999999999999999")
			w.Header().Set(headerFunctionARN, "arn:aws:lambda:us-east-1:123456789012:function:bench")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(testEvent{Name: "benchmark"})

		case "/2018-06-01/runtime/invocation/bench-request-id/response":
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)

	handler := func(ctx context.Context, event testEvent) (testResponse, error) {
		return testResponse{Message: "hello " + event.Name}, nil
	}

	b.ReportAllocs()

	for b.Loop() {
		if err := handleInvocation(client, handler, &options{logger: logger}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkHandleInvocation_WithMetadata measures overhead of Cognito/Client context parsing
func BenchmarkHandleInvocation_WithMetadata(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2018-06-01/runtime/invocation/next":
			w.Header().Set(headerRequestID, "bench-request-id")
			w.Header().Set(headerDeadlineMS, "999999999999999")
			w.Header().Set(headerFunctionARN, "arn:aws:lambda:us-east-1:123456789012:function:bench")
			w.Header().Set(headerTraceID, "Root=1-5e9c5b5f-1234567890abcdef")
			w.Header().Set(headerCognitoIdentity, `{"cognito_identity_id":"id-123","cognito_identity_pool_id":"pool-456"}`)
			w.Header().Set(headerClientContext, `{"client":{"installation_id":"install-789"},"custom":{"key":"value"}}`)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(testEvent{Name: "benchmark"})

		case "/2018-06-01/runtime/invocation/bench-request-id/response":
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)

	handler := func(ctx context.Context, event testEvent) (testResponse, error) {
		// Access context to ensure it's not optimized away
		lc, _ := FromContext(ctx)
		_ = lc
		return testResponse{Message: "hello " + event.Name}, nil
	}

	b.ReportAllocs()

	for b.Loop() {
		if err := handleInvocation(client, handler, &options{logger: logger}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkJSONMarshalUnmarshal measures JSON operations in isolation
func BenchmarkJSONMarshalUnmarshal(b *testing.B) {
	event := testEvent{Name: "benchmark"}
	response := testResponse{Message: "hello benchmark"}

	b.Run("Unmarshal", func(b *testing.B) {
		eventJSON, _ := json.Marshal(event)
		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			var e testEvent
			if err := json.Unmarshal(eventJSON, &e); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("Marshal", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()

		for b.Loop() {
			if _, err := json.Marshal(response); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkRuntimeClientNext measures the overhead of fetching next invocation
func BenchmarkRuntimeClientNext(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerRequestID, "bench-request-id")
		w.Header().Set(headerDeadlineMS, "999999999999999")
		w.Header().Set(headerFunctionARN, "arn:aws:lambda:us-east-1:123456789012:function:bench")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(testEvent{Name: "benchmark"})
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)

	b.ReportAllocs()

	for b.Loop() {
		inv, err := client.next()
		if err != nil {
			b.Fatal(err)
		}
		_ = inv
	}
}

// BenchmarkRuntimeClientPost measures the overhead of posting responses
func BenchmarkRuntimeClientPost(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)
	responseJSON, _ := json.Marshal(testResponse{Message: "hello"})

	url := client.baseURL + "test-request-id" + responsePath

	b.ReportAllocs()

	for b.Loop() {
		if err := client.post(context.Background(), url, responseJSON); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkContextOperations measures context creation and value extraction
func BenchmarkContextOperations(b *testing.B) {
	lc := &LambdaContext{
		AwsRequestID:       "bench-request-id",
		InvokedFunctionArn: "arn:aws:lambda:us-east-1:123456789012:function:bench",
	}

	b.Run("NewContext", func(b *testing.B) {
		ctx := context.Background()
		b.ResetTimer()
		b.ReportAllocs()

		for b.Loop() {
			_ = NewContext(ctx, lc)
		}
	})

	b.Run("FromContext", func(b *testing.B) {
		ctx := NewContext(context.Background(), lc)
		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			_, _ = FromContext(ctx)
		}
	})
}

// BenchmarkCallHandler measures the handler invocation overhead
func BenchmarkCallHandler(b *testing.B) {
	ctx := context.Background()
	eventJSON, _ := json.Marshal(testEvent{Name: "benchmark"})

	handler := func(ctx context.Context, event testEvent) (testResponse, error) {
		return testResponse{Message: "hello " + event.Name}, nil
	}

	b.ReportAllocs()

	for b.Loop() {
		if _, err := callHandler(ctx, eventJSON, handler); err != nil {
			b.Fatal(err)
		}
	}
}
