package voker

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func TestWithInternalExtension(t *testing.T) {
	ext := InternalExtension{
		Name: "TestExtension",
		OnInit: func() error {
			return nil
		},
	}

	opts := &options{}
	WithInternalExtension(ext)(opts)

	if len(opts.extensions) != 1 {
		t.Fatalf("expected 1 extension, got %d", len(opts.extensions))
	}
	if opts.extensions[0].Name != "TestExtension" {
		t.Errorf("expected extension name TestExtension, got %s", opts.extensions[0].Name)
	}
}

func TestWithInternalExtension_Multiple(t *testing.T) {
	ext1 := InternalExtension{Name: "Extension1"}
	ext2 := InternalExtension{Name: "Extension2"}

	opts := &options{}
	WithInternalExtension(ext1)(opts)
	WithInternalExtension(ext2)(opts)

	if len(opts.extensions) != 2 {
		t.Fatalf("expected 2 extensions, got %d", len(opts.extensions))
	}
	if opts.extensions[0].Name != "Extension1" {
		t.Errorf("expected first extension name Extension1, got %s", opts.extensions[0].Name)
	}
	if opts.extensions[1].Name != "Extension2" {
		t.Errorf("expected second extension name Extension2, got %s", opts.extensions[1].Name)
	}
}

func TestExtensionManager_Start_OnInit(t *testing.T) {
	initCalled := false
	ext := InternalExtension{
		Name: "TestExtension",
		OnInit: func() error {
			initCalled = true
			return nil
		},
	}

	// Mock server that simulates Extensions API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2020-01-01/extension/register":
			w.Header().Set(headerExtensionIdentifier, "test-id")
			w.WriteHeader(http.StatusOK)
		case "/2020-01-01/extension/event/next":
			// Block to prevent tight loop, server will close to end test
			time.Sleep(10 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}
	}))

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := newExtensionManager(server.Listener.Addr().String(), []InternalExtension{ext}, logger)
	err := mgr.start()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !initCalled {
		t.Error("expected OnInit to be called")
	}

	// Close server to terminate event loop
	server.Close()
	time.Sleep(50 * time.Millisecond)
}

func TestExtensionManager_Start_OnInitError(t *testing.T) {
	ext := InternalExtension{
		Name: "TestExtension",
		OnInit: func() error {
			return &ErrorResponse{Message: "init failed", Type: "ExtensionError"}
		},
	}

	// Mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerExtensionIdentifier, "test-id")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := newExtensionManager(server.Listener.Addr().String(), []InternalExtension{ext}, logger)
	err := mgr.start()

	if err == nil {
		t.Fatal("expected error from OnInit, got nil")
	}
}

func TestExtensionManager_Start_RegistersEvents(t *testing.T) {
	tests := []struct {
		name           string
		extension      InternalExtension
		expectedEvents []extensionEventType
	}{
		{
			name: "OnInvoke only",
			extension: InternalExtension{
				Name:     "InvokeOnly",
				OnInvoke: func(ctx context.Context, eventPayload ExtensionEventPayload) {},
			},
			expectedEvents: []extensionEventType{eventTypeInvoke},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedEvents []extensionEventType
			var mu sync.Mutex

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/2020-01-01/extension/register":
					// Capture registered events
					var req registerRequest
					json.NewDecoder(r.Body).Decode(&req)
					mu.Lock()
					receivedEvents = req.Events
					mu.Unlock()

					w.Header().Set(headerExtensionIdentifier, "test-id")
					w.WriteHeader(http.StatusOK)
				case "/2020-01-01/extension/event/next":
					// Block to prevent tight loop
					time.Sleep(10 * time.Millisecond)
					w.WriteHeader(http.StatusOK)
				}
			}))

			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
			mgr := newExtensionManager(server.Listener.Addr().String(), []InternalExtension{tt.extension}, logger)
			err := mgr.start()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Give it a moment to register
			time.Sleep(50 * time.Millisecond)

			mu.Lock()
			if len(receivedEvents) != len(tt.expectedEvents) {
				t.Errorf("expected %d events, got %d", len(tt.expectedEvents), len(receivedEvents))
			}
			for i, expected := range tt.expectedEvents {
				if i >= len(receivedEvents) {
					break
				}
				if receivedEvents[i] != expected {
					t.Errorf("expected event %s at index %d, got %s", expected, i, receivedEvents[i])
				}
			}
			mu.Unlock()

			// Close server to terminate event loop
			server.Close()
			time.Sleep(50 * time.Millisecond)
		})
	}
}

func TestExtensionManager_EventLoop_OnInvoke(t *testing.T) {
	invokeCalled := false
	var invokeCtx context.Context
	var mu sync.Mutex

	ext := InternalExtension{
		Name: "TestExtension",
		OnInvoke: func(ctx context.Context, eventPayload ExtensionEventPayload) {
			mu.Lock()
			defer mu.Unlock()
			invokeCalled = true
			invokeCtx = ctx
		},
	}

	eventsSent := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2020-01-01/extension/register":
			w.Header().Set(headerExtensionIdentifier, "test-id")
			w.WriteHeader(http.StatusOK)
		case "/2020-01-01/extension/event/next":
			eventsSent++
			if eventsSent == 1 {
				// Send INVOKE event
				event := ExtensionEventPayload{
					EventType:  eventTypeInvoke,
					DeadlineMs: time.Now().Add(time.Second).UnixMilli(),
					RequestID:  "test-request-id",
				}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(event)
			} else {
				// Block to prevent tight loop
				time.Sleep(10 * time.Millisecond)
				w.WriteHeader(http.StatusOK)
			}
		}
	}))

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := newExtensionManager(server.Listener.Addr().String(), []InternalExtension{ext}, logger)
	err := mgr.start()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Wait for event loop to process
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	if !invokeCalled {
		t.Error("expected OnInvoke to be called")
	}
	if invokeCtx == nil {
		t.Error("expected context to be passed to OnInvoke")
	}
	if _, ok := invokeCtx.Deadline(); !ok {
		t.Error("expected context to have deadline")
	}
	mu.Unlock()

	// Close server to terminate event loop
	server.Close()
	time.Sleep(50 * time.Millisecond)
}

func TestExtensionManager_Shutdown(t *testing.T) {
	sigtermCalled := false
	var sigtermCtx context.Context
	var mu sync.Mutex

	ext := InternalExtension{
		Name: "TestExtension",
		OnSIGTERM: func(ctx context.Context) {
			mu.Lock()
			defer mu.Unlock()
			sigtermCalled = true
			sigtermCtx = ctx
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2020-01-01/extension/register":
			w.Header().Set(headerExtensionIdentifier, "test-id")
			w.WriteHeader(http.StatusOK)
		case "/2020-01-01/extension/event/next":
			// Block to prevent tight loop
			time.Sleep(10 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}
	}))

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mgr := newExtensionManager(server.Listener.Addr().String(), []InternalExtension{ext}, logger)
	err := mgr.start()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Call shutdown to simulate SIGTERM
	mgr.shutdown()

	mu.Lock()
	if !sigtermCalled {
		t.Error("expected OnSIGTERM to be called")
	}
	if sigtermCtx == nil {
		t.Error("expected context to be passed to OnSIGTERM")
	}
	if _, ok := sigtermCtx.Deadline(); !ok {
		t.Error("expected context to have deadline")
	}
	mu.Unlock()

	// Close server to terminate event loop
	server.Close()
	time.Sleep(50 * time.Millisecond)
}
