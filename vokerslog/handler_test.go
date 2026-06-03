package vokerslog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"testing/slogtest"

	"github.com/hotsock/voker"
	"github.com/hotsock/voker/vokerslog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandler(t *testing.T) {
	t.Run("slogtest", func(t *testing.T) {
		t.Run("JSON", func(t *testing.T) {
			buffer := new(bytes.Buffer)
			newHandler := func(t *testing.T) slog.Handler {
				t.Cleanup(buffer.Reset)

				return vokerslog.NewHandler(buffer, vokerslog.WithLevel(slog.LevelDebug), vokerslog.WithJSON())
			}

			result := func(t *testing.T) map[string]any {
				result := make(map[string]any)
				json.Unmarshal(buffer.Bytes(), &result)
				return result
			}

			slogtest.Run(t, newHandler, result)
		})

		t.Run("Text", func(t *testing.T) {
			buffer := new(bytes.Buffer)
			newHandler := func(t *testing.T) slog.Handler {
				t.Cleanup(buffer.Reset)

				return vokerslog.NewHandler(buffer, vokerslog.WithLevel(slog.LevelDebug), vokerslog.WithText())
			}

			result := func(t *testing.T) map[string]any {
				result := make(map[string]any)

				unquote := func(s string) string {
					if len(s) == 0 || s[0] != '"' {
						return s
					}

					s, err := strconv.Unquote(s)
					require.NoError(t, err)
					return s
				}

				for entry := range strings.SplitSeq(strings.TrimSpace(buffer.String()), " ") {
					parts := strings.SplitN(entry, "=", 2)
					path := strings.Split(parts[0], ".")
					if len(path) == 1 {
						result[path[0]] = unquote(parts[1])
						continue
					}

					v := result

					for i := 0; i < len(path)-1; i++ {
						if _, ok := v[path[i]]; !ok {
							v[path[i]] = make(map[string]any)
						}
						v = v[path[i]].(map[string]any)
					}

					v[path[len(path)-1]] = unquote(parts[1])
				}

				return result
			}

			slogtest.Run(t, newHandler, result)
		})
	})

	t.Run("WithoutTime", func(t *testing.T) {
		t.Run("JSON", func(t *testing.T) {
			buffer := new(bytes.Buffer)
			logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON(), vokerslog.WithoutTime()))

			logger.Info(t.Name())

			assert.NotContains(t, buffer.String(), `"time"`)
		})

		t.Run("Text", func(t *testing.T) {
			buffer := new(bytes.Buffer)
			logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithText(), vokerslog.WithoutTime()))

			logger.Info(t.Name())

			assert.NotContains(t, buffer.String(), `time=`)
		})
	})

	t.Run("WithSource", func(t *testing.T) {
		t.Run("JSON", func(t *testing.T) {
			buffer := new(bytes.Buffer)
			logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON(), vokerslog.WithSource()))

			logger.Info(t.Name())

			assert.Contains(t, buffer.String(), `"source":{`)
		})

		t.Run("Text", func(t *testing.T) {
			buffer := new(bytes.Buffer)
			logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithText(), vokerslog.WithSource()))

			logger.Info(t.Name())

			assert.Contains(t, buffer.String(), `source.function=`)
			assert.Contains(t, buffer.String(), `source.file=`)
			assert.Contains(t, buffer.String(), `source.line=`)
		})
	})

	t.Run("WithType", func(t *testing.T) {
		t.Run("JSON", func(t *testing.T) {
			buffer := new(bytes.Buffer)
			logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON(), vokerslog.WithType(t.Name())))

			logger.Info(t.Name())

			assert.Contains(t, buffer.String(), `"type":"`+t.Name()+`"`)
		})

		t.Run("Text", func(t *testing.T) {
			buffer := new(bytes.Buffer)
			logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithText(), vokerslog.WithType(t.Name())))

			logger.Info(t.Name())

			assert.Contains(t, buffer.String(), `type="`+t.Name()+`"`)
		})
	})

	t.Run("given a lambda context", func(t *testing.T) {
		ctx := voker.NewContext(context.Background(), &voker.LambdaContext{
			AwsRequestID: "abc-123",
		})

		t.Run("JSON", func(t *testing.T) {
			buffer := new(bytes.Buffer)
			logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON()))

			logger.InfoContext(ctx, t.Name())

			assert.Contains(t, buffer.String(), `"requestId":"abc-123"`)
		})

		t.Run("Text", func(t *testing.T) {
			buffer := new(bytes.Buffer)
			logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithText()))

			logger.InfoContext(ctx, t.Name())

			assert.Contains(t, buffer.String(), `record.requestId="abc-123"`)
		})
	})

	t.Run("emits a deterministic JSON shape", func(t *testing.T) {
		buffer := new(bytes.Buffer)
		logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON(), vokerslog.WithoutTime()))

		logger.Info("hello", "n", 1, "ok", true)

		assert.JSONEq(t, `{"level":"INFO","msg":"hello","type":"app.log","n":1,"ok":true}`, buffer.String())
	})

	t.Run("nests groups and omits empty ones", func(t *testing.T) {
		buffer := new(bytes.Buffer)
		base := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON(), vokerslog.WithoutTime()))

		logger := base.WithGroup("outer").With("a", 1).WithGroup("inner")
		logger.Info("hi", "b", 2)

		out := buffer.String()
		assert.JSONEq(t, `{"level":"INFO","msg":"hi","type":"app.log","outer":{"a":1,"inner":{"b":2}}}`, out)

		// A trailing group with no attributes is omitted entirely.
		buffer.Reset()
		base.WithGroup("empty").Info("hi")
		assert.NotContains(t, buffer.String(), "empty")
	})

	t.Run("encodes a json.Marshaler as raw JSON", func(t *testing.T) {
		buffer := new(bytes.Buffer)
		logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON(), vokerslog.WithoutTime()))

		logger.Info("hi", "payload", rawMarshaler{})

		assert.Contains(t, buffer.String(), `"payload":{"nested":true}`)
	})

	t.Run("a per-record type attribute overrides the default", func(t *testing.T) {
		buffer := new(bytes.Buffer)
		logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON(), vokerslog.WithoutTime()))

		logger.Info("hi", vokerslog.TypeKey, "app.request", "n", 1)

		// The type field is overridden and the attribute is not also emitted.
		assert.JSONEq(t, `{"level":"INFO","msg":"hi","type":"app.request","n":1}`, buffer.String())
	})

	t.Run("a type set via WithAttrs applies to every record", func(t *testing.T) {
		buffer := new(bytes.Buffer)
		logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON(), vokerslog.WithoutTime())).
			With(vokerslog.TypeKey, "app.audit")

		logger.Info("hi")

		assert.JSONEq(t, `{"level":"INFO","msg":"hi","type":"app.audit"}`, buffer.String())
	})

	t.Run("a per-record type takes precedence over WithAttrs", func(t *testing.T) {
		buffer := new(bytes.Buffer)
		logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON(), vokerslog.WithoutTime())).
			With(vokerslog.TypeKey, "app.audit")

		logger.Info("hi", vokerslog.TypeKey, "app.request")

		assert.JSONEq(t, `{"level":"INFO","msg":"hi","type":"app.request"}`, buffer.String())
	})

	t.Run("an empty type omits the field", func(t *testing.T) {
		buffer := new(bytes.Buffer)
		logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON(), vokerslog.WithoutTime()))

		logger.Info("hi", vokerslog.TypeKey, "")

		assert.JSONEq(t, `{"level":"INFO","msg":"hi"}`, buffer.String())
	})

	t.Run("an empty message omits the msg field", func(t *testing.T) {
		t.Run("JSON", func(t *testing.T) {
			buffer := new(bytes.Buffer)
			logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON(), vokerslog.WithoutTime()))

			logger.Info("", "n", 1)

			assert.JSONEq(t, `{"level":"INFO","type":"app.log","n":1}`, buffer.String())
		})

		t.Run("Text", func(t *testing.T) {
			buffer := new(bytes.Buffer)
			logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithText(), vokerslog.WithoutTime()))

			logger.Info("", "n", 1)

			out := buffer.String()
			assert.NotContains(t, out, "msg=")
			assert.Contains(t, out, `level="INFO" `)
			assert.Contains(t, out, " n=1")
		})
	})

	t.Run("a type attribute inside a group is not an override", func(t *testing.T) {
		buffer := new(bytes.Buffer)
		logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON(), vokerslog.WithoutTime())).
			WithGroup("g")

		logger.Info("hi", vokerslog.TypeKey, "nope")

		// The default type stands; the grouped attribute is emitted normally.
		assert.JSONEq(t, `{"level":"INFO","msg":"hi","type":"app.log","g":{"type":"nope"}}`, buffer.String())
	})

	t.Run("includes function metadata from the environment", func(t *testing.T) {
		t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "my-func")
		t.Setenv("AWS_LAMBDA_FUNCTION_VERSION", "7")

		buffer := new(bytes.Buffer)
		logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON()))

		logger.Info("hi")

		assert.Contains(t, buffer.String(), `"record":{"functionName":"my-func","version":"7"}`)
	})

	t.Run("omits the version when the function is unpublished", func(t *testing.T) {
		t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "my-func")
		t.Setenv("AWS_LAMBDA_FUNCTION_VERSION", "$LATEST")

		buffer := new(bytes.Buffer)
		logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON()))

		logger.Info("hi")

		assert.Contains(t, buffer.String(), `"record":{"functionName":"my-func"}`)
		assert.NotContains(t, buffer.String(), "version")
	})
}

type rawMarshaler struct{}

func (rawMarshaler) MarshalJSON() ([]byte, error) {
	return []byte(`{"nested":true}`), nil
}

func BenchmarkJSON(b *testing.B) {
	logger := slog.New(vokerslog.NewHandler(io.Discard, vokerslog.WithJSON())).WithGroup("benchmark").With("format", "json")

	for i := 0; b.Loop(); i++ {
		logger.Info("test", "count", i)
	}
}

func BenchmarkText(b *testing.B) {
	logger := slog.New(vokerslog.NewHandler(io.Discard, vokerslog.WithText())).WithGroup("benchmark").With("format", "text")

	for i := 0; b.Loop(); i++ {
		logger.Info("test", "count", i)
	}
}
