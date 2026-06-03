package vokerslog

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func Test_loggerLevelFromString(t *testing.T) {
	cases := map[string]slog.Level{
		"TRACE":  slog.LevelDebug - 4,
		"DEBUG":  slog.LevelDebug,
		"INFO":   slog.LevelInfo,
		"WARN":   slog.LevelWarn,
		"ERROR":  slog.LevelError,
		"FATAL":  slog.LevelError + 4,
		"trace":  slog.LevelDebug - 4,
		"debug":  slog.LevelDebug,
		"info":   slog.LevelInfo,
		"Warn":   slog.LevelWarn,
		" error": slog.LevelError,
		" info ": slog.LevelInfo,
		"":       slog.LevelInfo,
	}

	for str, level := range cases {
		t.Run(fmt.Sprintf("%s=%s", str, &level), func(t *testing.T) {
			assert.Equal(t, level, loggerLevelFromString(str))
		})
	}
}

func Test_lambdaLoggerLevelString(t *testing.T) {
	cases := map[slog.Level]string{
		slog.LevelDebug - 8: "TRACE-4",
		slog.LevelDebug - 4: "TRACE",
		slog.LevelDebug:     "DEBUG",
		slog.LevelInfo:      "INFO",
		slog.LevelWarn:      "WARN",
		slog.LevelError:     "ERROR",
		slog.LevelError + 4: "FATAL",
		slog.LevelError + 8: "FATAL+4",
	}

	for level, str := range cases {
		t.Run(fmt.Sprintf("%s=%s", level, str), func(t *testing.T) {
			assert.Equal(t, str, lambdaLoggerLevelString(level))
		})
	}
}

func Test_appendJSONString(t *testing.T) {
	cases := map[string]string{
		"hello":          `"hello"`,
		`with "quotes"`:  `"with \"quotes\""`,
		`back\slash`:     `"back\\slash"`,
		"tab\tnewline\n": `"tab\tnewline\n"`,
		"carriage\r":     `"carriage\r"`,
		"<not>&escaped":  `"<not>&escaped"`, // '<', '>', and '&' are left unescaped
		"héllo":          `"héllo"`,         // valid UTF-8 copied verbatim
	}

	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			b := make(buffer, 0, 16)
			appendJSONString(&b, in)
			assert.Equal(t, want, string(b))
		})
	}

	t.Run("control characters are u-escaped", func(t *testing.T) {
		b := make(buffer, 0, 16)
		appendJSONString(&b, "unit\x01sep")
		assert.Equal(t, `"unit\u0001sep"`, string(b))
	})
}

func Test_appendJSONValue(t *testing.T) {
	cases := []struct {
		name  string
		value slog.Value
		want  string
	}{
		{"string", slog.StringValue("hi"), `"hi"`},
		{"int", slog.Int64Value(-7), `-7`},
		{"uint", slog.Uint64Value(7), `7`},
		{"float", slog.Float64Value(1.5), `1.5`},
		{"float NaN", slog.Float64Value(math.NaN()), `"NaN"`},
		{"float +Inf", slog.Float64Value(math.Inf(1)), `"+Inf"`},
		{"bool", slog.BoolValue(true), `true`},
		{"duration", slog.DurationValue(time.Second), `"1s"`},
		{"error", slog.AnyValue(errors.New("boom")), `"boom"`},
		{"nil", slog.AnyValue(nil), `null`},
		{"json.Marshaler", slog.AnyValue(jsonMarshalerSuccess{}), `"JSON Marshal"`},
		{"struct", slog.AnyValue(struct {
			A int `json:"a"`
		}{A: 1}), `{"a":1}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := make(buffer, 0, 16)
			appendJSONValue(&b, tc.value)
			assert.Equal(t, tc.want, string(b))
		})
	}
}

func Test_appendTextValue(t *testing.T) {
	cases := []struct {
		name  string
		value slog.Value
		want  string
	}{
		{"string", slog.StringValue("hi"), `"hi"`},
		{"int", slog.Int64Value(-7), `-7`},
		{"bool", slog.BoolValue(false), `false`},
		{"duration", slog.DurationValue(time.Second), `"1s"`},
		{"error", slog.AnyValue(errors.New("boom")), `"boom"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := make(buffer, 0, 16)
			appendTextValue(&b, tc.value)
			assert.Equal(t, tc.want, string(b))
		})
	}
}

type jsonMarshalerSuccess struct{}

func (jsonMarshalerSuccess) MarshalJSON() ([]byte, error) {
	return []byte(`"JSON Marshal"`), nil
}
