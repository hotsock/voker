package voker

import (
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
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

func TestCallHandler_RawPayload_VerbatimPayload(t *testing.T) {
	// Whitespace and key ordering must survive untouched, proving no
	// re-encoding happened.
	payload := []byte(`{  "b" :2,
		"a":1 }`)

	var got jsontext.Value
	handler := func(ctx context.Context, in jsontext.Value) (string, error) {
		got = in
		return "ok", nil
	}

	out, err := callHandler(context.Background(), payload, handler)
	require.NoError(t, err)
	assert.JSONEq(t, `"ok"`, string(out.payload))
	assert.Equal(t, string(payload), string(got))
}

func TestCallHandler_RawPayload_ZeroCopyAlias(t *testing.T) {
	payload := []byte(`{"large":"payload"}`)

	var got jsontext.Value
	handler := func(ctx context.Context, in jsontext.Value) (struct{}, error) {
		got = in
		return struct{}{}, nil
	}

	_, err := callHandler(context.Background(), payload, handler)
	require.NoError(t, err)

	// The handler must receive the exact same backing array, not a copy.
	assert.Equal(t, firstByte(payload), firstByte(got),
		"jsontext.Value input should alias the payload buffer, not copy it")
}

func TestCallHandler_RawPayload_InvalidJSONNotRejected(t *testing.T) {
	// The whole point of the bypass: invalid JSON is handed through instead of
	// being rejected with a Runtime.UnmarshalError.
	payload := []byte(`{not valid json`)

	called := false
	var got jsontext.Value
	handler := func(ctx context.Context, in jsontext.Value) (string, error) {
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

func TestCallHandler_RawPayload_EmptyPayload(t *testing.T) {
	called := false
	var got jsontext.Value
	handler := func(ctx context.Context, in jsontext.Value) (string, error) {
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

func TestCallHandler_RawPayload_NilPayload(t *testing.T) {
	got := jsontext.Value("stale")
	handler := func(ctx context.Context, in jsontext.Value) (string, error) {
		got = in
		return "ok", nil
	}

	_, err := callHandler(context.Background(), nil, handler)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestCallHandler_RawPayload_HandlerDecodesItself(t *testing.T) {
	// Realistic usage: the handler owns its own decoding (and could measure it).
	payload := []byte(`{"name":"voker"}`)

	handler := func(ctx context.Context, in jsontext.Value) (testResponse, error) {
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

func TestCallHandler_RawPayload_PointerInputUnaffected(t *testing.T) {
	// A *jsontext.Value input is NOT the bypass type; it must still go through
	// the normal unmarshal path (which validates).
	payload := []byte(`{not json`)

	handler := func(ctx context.Context, in *jsontext.Value) (string, error) {
		return "ok", nil
	}

	_, err := callHandler(context.Background(), payload, handler)
	require.Error(t, err, "*jsontext.Value should not trigger the raw bypass")
	var errResp *ErrorResponse
	require.ErrorAs(t, err, &errResp)
	assert.Equal(t, "Runtime.UnmarshalError", errResp.Type)
}

// json.RawMessage is a type alias for jsontext.Value as of Go 1.27, so
// handlers written against the classic type must hit the same bypass. The
// assignment below fails to compile if the two ever stop being identical.
var _ func(context.Context, jsontext.Value) (string, error) = func(context.Context, jsonv1.RawMessage) (string, error) {
	return "", nil
}

func TestCallHandler_RawMessageAlias_StillBypasses(t *testing.T) {
	payload := []byte(`{not valid json`)

	var got jsonv1.RawMessage
	handler := func(ctx context.Context, in jsonv1.RawMessage) (string, error) {
		got = in
		return "handled", nil
	}

	out, err := callHandler(context.Background(), payload, handler)
	require.NoError(t, err)
	assert.Equal(t, string(payload), string(got))
	assert.Equal(t, firstByte(payload), firstByte(got),
		"json.RawMessage input should alias the payload buffer, not copy it")
	assert.JSONEq(t, `"handled"`, string(out.payload))
}

func TestCallHandler_TypedInput_StillValidates(t *testing.T) {
	// Regression: non-raw handlers must keep rejecting invalid JSON.
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

// TestHandleInvocation_RawPayload_EndToEnd exercises the bypass through the
// full invocation loop, including a payload that is deliberately not valid
// JSON to confirm it reaches the handler instead of being rejected.
func TestHandleInvocation_RawPayload_EndToEnd(t *testing.T) {
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

	handler := func(ctx context.Context, in jsontext.Value) (string, error) {
		// Echo back exactly what we received.
		return string(in), nil
	}

	err := handleInvocation(client, handler, &options{logger: logger})
	require.NoError(t, err)
	require.True(t, responseReceived, "success response should be sent, not an error")
	assert.JSONEq(t, `"`+rawPayload+`"`, string(receivedResponse))
}

// BenchmarkCallHandler_RawPayload_1MB demonstrates the bypass: a ~1MB payload
// is handed to the handler without unmarshaling or validation.
func BenchmarkCallHandler_RawPayload_1MB(b *testing.B) {
	payload := makeLargeJSON(1 << 20)
	handler := func(ctx context.Context, in jsontext.Value) (struct{}, error) {
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
