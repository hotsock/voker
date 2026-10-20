package vokerslog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/slogtest"
	"time"

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
				require.NoError(t, json.Unmarshal(buffer.Bytes(), &result))
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

	t.Run("a nil WithLevel means INFO", func(t *testing.T) {
		buffer := new(bytes.Buffer)
		logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON(), vokerslog.WithLevel(nil)))

		logger.Debug("dropped")
		logger.Info("kept")

		assert.NotContains(t, buffer.String(), "dropped")
		assert.Contains(t, buffer.String(), "kept")
	})

	t.Run("a nil error pointer does not panic", func(t *testing.T) {
		buffer := new(bytes.Buffer)
		logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON(), vokerslog.WithoutTime()))

		logger.Info("hi", "error", (*nilError)(nil))

		assert.JSONEq(t, `{"level":"INFO","msg":"hi","type":"app.log","error":"<nil>"}`, buffer.String())
	})

	t.Run("omits a group holding only empty attrs", func(t *testing.T) {
		buffer := new(bytes.Buffer)
		logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON(), vokerslog.WithoutTime()))

		logger.Info("hi", slog.Group("a", slog.Attr{}), slog.Group("b", slog.Group("c", slog.Attr{})))

		assert.JSONEq(t, `{"level":"INFO","msg":"hi","type":"app.log"}`, buffer.String())
	})

	t.Run("quotes text keys and values only when needed", func(t *testing.T) {
		buffer := new(bytes.Buffer)
		logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithText(), vokerslog.WithoutTime()))

		logger.WithGroup("my group").Info("hi", "a b", 1, "c=d", 2, "plain", []string{"x"}, "spaced", []string{"x y"})

		assert.Equal(t, `level="INFO" msg="hi" type="app.log" "my group.a b"=1 "my group.c=d"=2 "my group.plain"=[x] "my group.spaced"="[x y]"`+"\n", buffer.String())
	})

	t.Run("resolves a LogValuer passed to WithAttrs once", func(t *testing.T) {
		buffer := new(bytes.Buffer)
		calls := 0
		logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON(), vokerslog.WithoutTime())).
			With("v", countingValuer{calls: &calls, value: slog.IntValue(1)})

		logger.Info("one")
		logger.Info("two")

		assert.Equal(t, 1, calls)
		assert.Equal(t, 2, strings.Count(buffer.String(), `"v":1`))
	})

	t.Run("resolves a record's type LogValuer once", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			value slog.Value
			want  string
		}{
			{"string overrides the type", slog.StringValue("app.counted"), `{"level":"INFO","msg":"hi","type":"app.counted"}`},
			{"non-string is an ordinary attr", slog.IntValue(7), `{"level":"INFO","msg":"hi","type":"app.log","type":7}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				buffer := new(bytes.Buffer)
				calls := 0
				logger := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON(), vokerslog.WithoutTime()))

				logger.Info("hi", vokerslog.TypeKey, countingValuer{calls: &calls, value: tc.value})

				assert.Equal(t, 1, calls)
				assert.Equal(t, tc.want+"\n", buffer.String())
			})
		}
	})

	t.Run("preformats WithAttrs and WithGroup chains", func(t *testing.T) {
		cases := []struct {
			name  string
			chain func(*slog.Logger) *slog.Logger
			args  []any
			json  string
			text  string
		}{
			{
				name:  "attrs then group then record attrs",
				chain: func(l *slog.Logger) *slog.Logger { return l.With("a", 1).WithGroup("g").With("b", 2) },
				args:  []any{"c", 3},
				json:  `{"level":"INFO","msg":"hi","type":"app.log","a":1,"g":{"b":2,"c":3}}`,
				text:  `level="INFO" msg="hi" type="app.log" a=1 g.b=2 g.c=3`,
			},
			{
				name:  "open group with no record attrs",
				chain: func(l *slog.Logger) *slog.Logger { return l.WithGroup("g").With("b", 2) },
				json:  `{"level":"INFO","msg":"hi","type":"app.log","g":{"b":2}}`,
				text:  `level="INFO" msg="hi" type="app.log" g.b=2`,
			},
			{
				name:  "pending groups opened by WithAttrs",
				chain: func(l *slog.Logger) *slog.Logger { return l.WithGroup("a").WithGroup("b").With("x", 1) },
				json:  `{"level":"INFO","msg":"hi","type":"app.log","a":{"b":{"x":1}}}`,
				text:  `level="INFO" msg="hi" type="app.log" a.b.x=1`,
			},
			{
				name:  "trailing pending group is dropped when the record is empty",
				chain: func(l *slog.Logger) *slog.Logger { return l.WithGroup("a").With("x", 1).WithGroup("b") },
				json:  `{"level":"INFO","msg":"hi","type":"app.log","a":{"x":1}}`,
				text:  `level="INFO" msg="hi" type="app.log" a.x=1`,
			},
			{
				name:  "trailing pending group holds the record attrs",
				chain: func(l *slog.Logger) *slog.Logger { return l.WithGroup("a").With("x", 1).WithGroup("b") },
				args:  []any{"y", 2},
				json:  `{"level":"INFO","msg":"hi","type":"app.log","a":{"x":1,"b":{"y":2}}}`,
				text:  `level="INFO" msg="hi" type="app.log" a.x=1 a.b.y=2`,
			},
			{
				name:  "empty WithAttrs leaves groups pending",
				chain: func(l *slog.Logger) *slog.Logger { return l.WithGroup("a").With(slog.Group("e")) },
				json:  `{"level":"INFO","msg":"hi","type":"app.log"}`,
				text:  `level="INFO" msg="hi" type="app.log"`,
			},
			{
				name:  "chain type is taken before a group",
				chain: func(l *slog.Logger) *slog.Logger { return l.With(vokerslog.TypeKey, "app.audit").WithGroup("g") },
				args:  []any{vokerslog.TypeKey, "nope"},
				json:  `{"level":"INFO","msg":"hi","type":"app.audit","g":{"type":"nope"}}`,
				text:  `level="INFO" msg="hi" type="app.audit" g.type="nope"`,
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				for _, format := range []struct {
					name   string
					option vokerslog.Option
					want   string
				}{
					{"JSON", vokerslog.WithJSON(), tc.json},
					{"Text", vokerslog.WithText(), tc.text},
				} {
					t.Run(format.name, func(t *testing.T) {
						buffer := new(bytes.Buffer)
						logger := tc.chain(slog.New(vokerslog.NewHandler(buffer, format.option, vokerslog.WithoutTime())))

						// Log twice to catch state leaking between records.
						logger.Info("hi", tc.args...)
						logger.Info("hi", tc.args...)

						line := format.want + "\n"
						assert.Equal(t, line+line, buffer.String())
					})
				}
			})
		}
	})

	t.Run("sibling loggers do not share chain state", func(t *testing.T) {
		buffer := new(bytes.Buffer)
		base := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON(), vokerslog.WithoutTime())).
			WithGroup("g").With("a", 1)
		x := base.With("x", 1).WithGroup("h")
		y := base.With("y", 2)

		x.Info("x", "k", 1)
		y.Info("y")
		base.Info("base")

		assert.Equal(t, `{"level":"INFO","msg":"x","type":"app.log","g":{"a":1,"x":1,"h":{"k":1}}}
{"level":"INFO","msg":"y","type":"app.log","g":{"a":1,"y":2}}
{"level":"INFO","msg":"base","type":"app.log","g":{"a":1}}
`, buffer.String())
	})

	t.Run("concurrent records are written whole", func(t *testing.T) {
		buffer := new(bytes.Buffer)
		base := slog.New(vokerslog.NewHandler(buffer, vokerslog.WithJSON())).With("service", "orders")

		const goroutines, records = 8, 100
		var wg sync.WaitGroup
		for g := range goroutines {
			logger := base.WithGroup("worker").With("id", g)
			wg.Go(func() {
				for i := range records {
					logger.Info("hi", "i", i)
				}
			})
		}
		wg.Wait()

		lines := strings.Split(strings.TrimSuffix(buffer.String(), "\n"), "\n")
		require.Len(t, lines, goroutines*records)
		for _, line := range lines {
			assert.True(t, json.Valid([]byte(line)), line)
		}
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

// nilError dereferences its receiver, so a typed nil panics in Error.
type nilError struct{ msg string }

func (e *nilError) Error() string { return e.msg }

// countingValuer counts how many times it is resolved.
type countingValuer struct {
	calls *int
	value slog.Value
}

func (v countingValuer) LogValue() slog.Value {
	*v.calls++
	return v.value
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

func BenchmarkJSONGroups(b *testing.B) {
	benchmarkGroups(b, vokerslog.WithJSON())
}

func BenchmarkTextGroups(b *testing.B) {
	benchmarkGroups(b, vokerslog.WithText())
}

func benchmarkGroups(b *testing.B, format vokerslog.Option) {
	ctx := voker.NewContext(context.Background(), &voker.LambdaContext{
		AwsRequestID: "abc-123",
	})
	logger := slog.New(vokerslog.NewHandler(io.Discard, format)).
		With("service", "orders", "region", "us-east-1").
		WithGroup("request").
		With("method", "POST", "path", "/orders")

	b.ReportAllocs()

	for i := 0; b.Loop(); i++ {
		logger.InfoContext(ctx, "handled",
			"status", 201,
			"duration", 42*time.Millisecond,
			slog.Group("user", "id", i, "admin", false, slog.Group("plan", "name", "pro", "seats", 5)),
			slog.Group("empty"),
			"ratio", 0.75,
		)
	}
}

func BenchmarkJSONAny(b *testing.B) {
	benchmarkAny(b, vokerslog.WithJSON())
}

func BenchmarkTextAny(b *testing.B) {
	benchmarkAny(b, vokerslog.WithText())
}

func benchmarkAny(b *testing.B, format vokerslog.Option) {
	type order struct {
		ID    string   `json:"id"`
		Items []string `json:"items"`
		Total float64  `json:"total"`
	}
	logger := slog.New(vokerslog.NewHandler(io.Discard, format))
	o := order{ID: "ord-1", Items: []string{"a", "b", "c"}, Total: 12.5}

	b.ReportAllocs()

	for b.Loop() {
		logger.Info("order", "order", o)
	}
}
