package voker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExtensionAPIClient_Register(t *testing.T) {
	extensionID := "test-extension-id-12345"
	extensionName := "TestExtension"
	requestedEvents := []extensionEventType{eventTypeInvoke}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method and path
		if r.Method != http.MethodPost {
			t.Errorf("expected POST request, got %s", r.Method)
		}
		if r.URL.Path != "/2020-01-01/extension/register" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		// Verify extension name header
		if name := r.Header.Get(headerExtensionName); name != extensionName {
			t.Errorf("expected extension name %s, got %s", extensionName, name)
		}

		// Verify request body
		var req registerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if len(req.Events) != len(requestedEvents) {
			t.Errorf("expected %d events, got %d", len(requestedEvents), len(req.Events))
		}

		// Send successful response
		w.Header().Set(headerExtensionIdentifier, extensionID)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newExtensionAPIClient(server.Listener.Addr().String())
	id, err := client.register(extensionName, requestedEvents)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != extensionID {
		t.Errorf("expected extension ID %s, got %s", extensionID, id)
	}
}

func TestExtensionAPIClient_Register_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := newExtensionAPIClient(server.Listener.Addr().String())
	_, err := client.register("TestExtension", []extensionEventType{eventTypeInvoke})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestExtensionAPIClient_Next(t *testing.T) {
	extensionID := "test-extension-id-12345"
	expectedEvent := ExtensionEventPayload{
		EventType:          eventTypeInvoke,
		DeadlineMs:         1234567890,
		RequestID:          "test-request-id",
		InvokedFunctionArn: "arn:aws:lambda:us-east-1:123456789012:function:test",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method and path
		if r.Method != http.MethodGet {
			t.Errorf("expected GET request, got %s", r.Method)
		}
		if r.URL.Path != "/2020-01-01/extension/event/next" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		// Verify extension identifier header
		if id := r.Header.Get(headerExtensionIdentifier); id != extensionID {
			t.Errorf("expected extension ID %s, got %s", extensionID, id)
		}

		// Send event response
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedEvent)
	}))
	defer server.Close()

	client := newExtensionAPIClient(server.Listener.Addr().String())
	event, err := client.next(extensionID)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.EventType != expectedEvent.EventType {
		t.Errorf("expected event type %s, got %s", expectedEvent.EventType, event.EventType)
	}
	if event.DeadlineMs != expectedEvent.DeadlineMs {
		t.Errorf("expected deadline %d, got %d", expectedEvent.DeadlineMs, event.DeadlineMs)
	}
	if event.RequestID != expectedEvent.RequestID {
		t.Errorf("expected request ID %s, got %s", expectedEvent.RequestID, event.RequestID)
	}
}

func TestExtensionAPIClient_Next_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := newExtensionAPIClient(server.Listener.Addr().String())
	_, err := client.next("test-id")

	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
