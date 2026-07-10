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
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// firstByte returns the address of the backing array's first byte, or nil for
// an empty slice. Used to prove the bypass aliases the payload rather than
// copying it.
func firstByte(b []byte) unsafe.Pointer {
	if len(b) == 0 {
		return nil
	}
	return unsafe.Pointer(&b[0])
}

func TestCallHandler_RawMessage_VerbatimPayload(t *testing.T) {
	// Whitespace and key ordering must survive untouched, proving no
	// re-encoding happened.
	payload := []byte(`{  "b" :2,
		"a":1 }`)

	var got json.RawMessage
	handler := func(ctx context.Context, in json.RawMessage) (string, error) {
		got = in
		return "ok", nil
	}

	out, err := callHandler(context.Background(), payload, handler)
	require.NoError(t, err)
	assert.JSONEq(t, `"ok"`, string(out.payload))
	assert.Equal(t, string(payload), string(got))
}

func TestCallHandler_RawMessage_ZeroCopyAlias(t *testing.T) {
	payload := []byte(`{"large":"payload"}`)

	var got json.RawMessage
	handler := func(ctx context.Context, in json.RawMessage) (struct{}, error) {
		got = in
		return struct{}{}, nil
	}

	_, err := callHandler(context.Background(), payload, handler)
	require.NoError(t, err)

	// The handler must receive the exact same backing array, not a copy.
	assert.Equal(t, firstByte(payload), firstByte(got),
		"json.RawMessage input should alias the payload buffer, not copy it")
}

func TestCallHandler_RawMessage_InvalidJSONNotRejected(t *testing.T) {
	// The whole point of the bypass: invalid JSON is handed through instead of
	// being rejected with a Runtime.UnmarshalError.
	payload := []byte(`{not valid json`)

	called := false
	var got json.RawMessage
	handler := func(ctx context.Context, in json.RawMessage) (string, error) {
		called = true
		got = in
		return "handled", nil
	}

	out, err := callHandler(context.Background(), payload, handler)
	require.NoError(t, err)
	assert.True(t, called, "handler should run even with non-JSON payload")
	assert.Equal(t, string(payload), string(got))
	assert.JSONEq(t, `"handled"`, string(out.payload))
}

func TestCallHandler_RawMessage_EmptyPayload(t *testing.T) {
	called := false
	var got json.RawMessage
	handler := func(ctx context.Context, in json.RawMessage) (string, error) {
		called = true
		got = in
		return "ok", nil
	}

	out, err := callHandler(context.Background(), []byte{}, handler)
	require.NoError(t, err)
	assert.True(t, called, "handler should run on an empty payload instead of erroring")
	assert.Empty(t, got)
	assert.JSONEq(t, `"ok"`, string(out.payload))
}

func TestCallHandler_RawMessage_NilPayload(t *testing.T) {
	var got json.RawMessage = json.RawMessage("stale")
	handler := func(ctx context.Context, in json.RawMessage) (string, error) {
		got = in
		return "ok", nil
	}

	_, err := callHandler(context.Background(), nil, handler)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestCallHandler_RawMessage_HandlerDecodesItself(t *testing.T) {
	// Realistic usage: the handler owns its own decoding (and could measure it).
	payload := []byte(`{"name":"voker"}`)

	handler := func(ctx context.Context, in json.RawMessage) (testResponse, error) {
		var ev testEvent
		if err := json.Unmarshal(in, &ev); err != nil {
			return testResponse{}, err
		}
		return testResponse{Message: "hello " + ev.Name}, nil
	}

	out, err := callHandler(context.Background(), payload, handler)
	require.NoError(t, err)
	assert.JSONEq(t, `{"message":"hello voker"}`, string(out.payload))
}

func TestCallHandler_RawMessage_PointerInputUnaffected(t *testing.T) {
	// A *json.RawMessage input is NOT the bypass type; it must still go through
	// the normal unmarshal path (which validates).
	payload := []byte(`{not json`)

	handler := func(ctx context.Context, in *json.RawMessage) (string, error) {
		return "ok", nil
	}

	_, err := callHandler(context.Background(), payload, handler)
	require.Error(t, err, "*json.RawMessage should not trigger the raw bypass")
	var errResp *ErrorResponse
	require.ErrorAs(t, err, &errResp)
	assert.Equal(t, "Runtime.UnmarshalError", errResp.Type)
}

func TestCallHandler_TypedInput_StillValidates(t *testing.T) {
	// Regression: non-RawMessage handlers must keep rejecting invalid JSON.
	payload := []byte(`{not json`)

	handler := func(ctx context.Context, in testEvent) (string, error) {
		t.Fatal("handler should not be called for invalid JSON")
		return "", nil
	}

	_, err := callHandler(context.Background(), payload, handler)
	require.Error(t, err)
	var errResp *ErrorResponse
	require.ErrorAs(t, err, &errResp)
	assert.Equal(t, "Runtime.UnmarshalError", errResp.Type)
	assert.Contains(t, errResp.Message, "failed to unmarshal input")
}

func TestCallHandler_TypedInput_StillUnmarshals(t *testing.T) {
	// Regression: the common typed path is unchanged.
	payload := []byte(`{"name":"world"}`)

	handler := func(ctx context.Context, in testEvent) (testResponse, error) {
		return testResponse{Message: "hi " + in.Name}, nil
	}

	out, err := callHandler(context.Background(), payload, handler)
	require.NoError(t, err)
	assert.JSONEq(t, `{"message":"hi world"}`, string(out.payload))
}

// TestHandleInvocation_RawMessage_EndToEnd exercises the bypass through the
// full invocation loop, including a payload that is deliberately not valid
// JSON to confirm it reaches the handler instead of being rejected.
func TestHandleInvocation_RawMessage_EndToEnd(t *testing.T) {
	const rawPayload = `this is not json at all`

	responseReceived := false
	var receivedResponse []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2018-06-01/runtime/invocation/next":
			w.Header().Set(headerRequestID, "raw-request-id")
			w.Header().Set(headerDeadlineMS, "999999999999999")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(rawPayload))

		case "/2018-06-01/runtime/invocation/raw-request-id/response":
			responseReceived = true
			receivedResponse, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := newRuntimeClient(server.URL[7:], logger)

	handler := func(ctx context.Context, in json.RawMessage) (string, error) {
		// Echo back exactly what we received.
		return string(in), nil
	}

	err := handleInvocation(client, handler, &options{logger: logger})
	require.NoError(t, err)
	require.True(t, responseReceived, "success response should be sent, not an error")
	assert.JSONEq(t, `"`+rawPayload+`"`, string(receivedResponse))
}

// BenchmarkCallHandler_RawMessage_1MB demonstrates the bypass: a ~1MB payload
// is handed to the handler without unmarshaling or validation.
func BenchmarkCallHandler_RawMessage_1MB(b *testing.B) {
	payload := makeLargeJSON(1 << 20)
	handler := func(ctx context.Context, in json.RawMessage) (struct{}, error) {
		return struct{}{}, nil
	}
	ctx := context.Background()

	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := callHandler(ctx, payload, handler); err != nil {
			b.Fatal(err)
		}
	}
}

func makeLargeJSON(approxSize int) []byte {
	out := []byte(`{"data":"`)
	for len(out) < approxSize {
		out = append(out, 'x')
	}
	return append(out, '"', '}')
}
