package vokerhttp

import (
	"bytes"
	"net/http"
	"testing"
)

// BenchmarkBufferedResponseBody measures converting a buffered handler
// response into a Lambda response body, the hot path for every buffered
// (non-streaming) vokerhttp invocation.
func BenchmarkBufferedResponseBody(b *testing.B) {
	payload := bytes.Repeat([]byte("a"), 1<<20)

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))

	for b.Loop() {
		w := newBufferedResponseWriter()
		w.Header().Set("Content-Type", "text/plain")
		if _, err := w.Write(payload); err != nil {
			b.Fatal(err)
		}
		body, isBase64, err := responseBody(w.result())
		if err != nil {
			b.Fatal(err)
		}
		if isBase64 || len(body) != len(payload) {
			b.Fatal("unexpected response body")
		}
	}
}

// TestReadFullBody covers the ContentLength fast path and its fallbacks.
func TestReadFullBody(t *testing.T) {
	t.Run("known length reads in one buffer", func(t *testing.T) {
		w := newBufferedResponseWriter()
		if _, err := w.Write([]byte("hello world")); err != nil {
			t.Fatal(err)
		}
		got, err := readFullBody(w.result())
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "hello world" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("unset ContentLength falls back to ReadAll", func(t *testing.T) {
		resp := &http.Response{Body: http.NoBody}
		resp.Body = readCloser{bytes.NewReader([]byte("hand-built"))}
		got, err := readFullBody(resp)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "hand-built" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("unknown ContentLength falls back to ReadAll", func(t *testing.T) {
		resp := &http.Response{ContentLength: -1, Body: readCloser{bytes.NewReader([]byte("streamed"))}}
		got, err := readFullBody(resp)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "streamed" {
			t.Fatalf("got %q", got)
		}
	})
}

type readCloser struct{ *bytes.Reader }

func (readCloser) Close() error { return nil }
