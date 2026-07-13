// Package vokerslog provides a [slog.Handler] tuned for AWS Lambda functions.
//
// It auto-configures log format (JSON or text) and level from Lambda's
// advanced logging environment variables, and enriches every record with
// Lambda metadata (function name, version, and the request ID from the
// invocation context). Options override the environment values.
//
// See https://docs.aws.amazon.com/lambda/latest/dg/monitoring-logs.html
//
// Usage:
//
//	logger := slog.New(vokerslog.NewHandler(os.Stderr))
//	slog.SetDefault(logger)
//	voker.Start(handler, voker.WithLogger(logger))
package vokerslog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hotsock/voker"
)

const (
	lambdaEnvLogLevel        = "AWS_LAMBDA_LOG_LEVEL"
	lambdaEnvLogFormat       = "AWS_LAMBDA_LOG_FORMAT"
	lambdaEnvFunctionName    = "AWS_LAMBDA_FUNCTION_NAME"
	lambdaEnvFunctionVersion = "AWS_LAMBDA_FUNCTION_VERSION"

	// lambdaLatestVersion is Lambda's name for the unpublished version, invoked
	// implicitly for any unqualified ARN. It carries no information a real
	// published version number wouldn't, so it is omitted from log records.
	lambdaLatestVersion = "$LATEST"

	traceLevelDebugOffset = 4
	fatalLevelErrorOffset = 4
)

// Handler implements [slog.Handler] and writes structured log records to an
// [io.Writer]. Use [NewHandler] to create one.
type Handler struct {
	out         io.Writer
	logType     string
	mu          *sync.Mutex
	level       slog.Leveler
	json        bool
	source      bool
	excludeTime bool

	// Lambda environment metadata, captured once at construction since it is
	// fixed for the lifetime of the process.
	functionName       string
	functionVersion    string
	hasFunctionName    bool
	hasFunctionVersion bool

	gattr []groupOrAttrs
}

// Option configures a [Handler]. Pass options to [NewHandler].
type Option func(*Handler)

// WithLevel sets the minimum log level. Messages below this level are dropped.
// Defaults to the value of AWS_LAMBDA_LOG_LEVEL, or INFO if unset.
func WithLevel(level slog.Leveler) Option {
	return func(h *Handler) {
		h.level = level
	}
}

// WithJSON sets the output format to JSON.
func WithJSON() Option {
	return func(h *Handler) {
		h.json = true
	}
}

// WithText sets the output format to key=value text.
func WithText() Option {
	return func(h *Handler) {
		h.json = false
	}
}

// WithSource adds caller information (function, file, line) to each log record.
func WithSource() Option {
	return func(h *Handler) {
		h.source = true
	}
}

// WithType sets the "type" field included in every log record.
// Defaults to "app.log".
//
// The "type" + "record" envelope mirrors the shape of AWS Lambda Telemetry API
// events, so "type" is best used as a low-cardinality category for filtering
// and routing logs (for example in CloudWatch Logs Insights), not for
// per-request data. AWS reserves the values "function", "extension", and
// "platform.*"; a dotted "app.<category>" namespace (such as "app.log",
// "app.request", or "app.audit") avoids collisions and matches AWS's style.
//
// An individual record can override this default with a top-level string
// attribute named [TypeKey]; setting it to "" omits the field for that record.
func WithType(logType string) Option {
	return func(h *Handler) {
		h.logType = logType
	}
}

// TypeKey is the attribute key that overrides a record's "type" field. A
// top-level (not nested in a group) string attribute with this key sets the
// type for that record instead of being emitted as an ordinary attribute.
// It may be set per call or, via WithAttrs, for every record from a logger:
//
//	requests := slog.New(handler).With(vokerslog.TypeKey, "app.request")
//
// A per-call value takes precedence over one set via WithAttrs. See [WithType].
const TypeKey = "type"

// WithoutTime omits the timestamp from log output. Useful for testing or when
// CloudWatch already provides timestamps.
func WithoutTime() Option {
	return func(h *Handler) {
		h.excludeTime = true
	}
}

// NewHandler returns a [Handler] that writes to w.
//
// By default it reads AWS_LAMBDA_LOG_LEVEL (TRACE, DEBUG, INFO, WARN, ERROR,
// FATAL) and AWS_LAMBDA_LOG_FORMAT (json, text) from the environment. Any
// provided options take precedence over environment values.
func NewHandler(w io.Writer, options ...Option) *Handler {
	h := &Handler{
		out:     w,
		mu:      new(sync.Mutex),
		level:   loggerLevelFromLambdaEnv(),
		json:    loggerIsJSON(),
		logType: "app.log",
	}
	h.functionName, h.hasFunctionName = os.LookupEnv(lambdaEnvFunctionName)
	h.functionVersion, h.hasFunctionVersion = os.LookupEnv(lambdaEnvFunctionVersion)
	if h.functionVersion == lambdaLatestVersion {
		h.hasFunctionVersion = false
	}

	for _, opt := range options {
		opt(h)
	}

	return h
}

func loggerLevelFromLambdaEnv() slog.Level {
	return loggerLevelFromString(os.Getenv(lambdaEnvLogLevel))
}

func loggerLevelFromString(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "trace":
		return slog.LevelDebug - traceLevelDebugOffset
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "fatal":
		return slog.LevelError + fatalLevelErrorOffset
	default:
		return slog.LevelInfo
	}
}

func lambdaLoggerLevelString(l slog.Level) string {
	str := func(base string, val slog.Level) string {
		if val == 0 {
			return base
		}
		return fmt.Sprintf("%s%+d", base, val)
	}

	switch {
	case l < slog.LevelDebug:
		return str("TRACE", l-(slog.LevelDebug-traceLevelDebugOffset))
	case l < slog.LevelInfo:
		return str("DEBUG", l-slog.LevelDebug)
	case l < slog.LevelWarn:
		return str("INFO", l-slog.LevelInfo)
	case l < slog.LevelError:
		return str("WARN", l-slog.LevelWarn)
	case l < slog.LevelError+fatalLevelErrorOffset:
		return str("ERROR", l-slog.LevelError)
	default:
		return str("FATAL", l-(slog.LevelError+fatalLevelErrorOffset))
	}
}

func loggerIsJSON() bool {
	env := os.Getenv(lambdaEnvLogFormat)
	return strings.ToLower(strings.TrimSpace(env)) == "json"
}

func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return h.copy(groupOrAttrs{attrs: attrs})
}

func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		// slog requires WithGroup("") to be a no-op.
		return h
	}
	return h.copy(groupOrAttrs{group: name})
}

func (h *Handler) copy(g groupOrAttrs) *Handler {
	c := *h
	c.gattr = make([]groupOrAttrs, len(h.gattr)+1)
	copy(c.gattr, h.gattr)
	c.gattr[len(c.gattr)-1] = g
	return &c
}

// groupOrAttrs holds either a group name (from WithGroup) or a set of
// attributes (from WithAttrs).
type groupOrAttrs struct {
	group string      // group name if non-empty
	attrs []slog.Attr // attrs if non-empty
}

func (h *Handler) Handle(ctx context.Context, record slog.Record) error {
	buf := newBuffer()
	defer freeBuffer(buf)

	if h.json {
		h.appendJSON(buf, ctx, record)
	} else {
		h.appendText(buf, ctx, record)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	_, err := h.out.Write(*buf)
	return err
}

var _ slog.Handler = (*Handler)(nil)

// --- JSON encoding -----------------------------------------------------------

func (h *Handler) appendJSON(buf *buffer, ctx context.Context, record slog.Record) {
	buf.writeString(`{"level":`)
	appendJSONString(buf, lambdaLoggerLevelString(record.Level))
	if record.Message != "" {
		buf.writeString(`,"msg":`)
		appendJSONString(buf, record.Message)
	}

	if !record.Time.IsZero() && !h.excludeTime {
		buf.writeString(`,"time":`)
		buf.writeByte('"')
		*buf = record.Time.AppendFormat(*buf, time.RFC3339Nano)
		buf.writeByte('"')
	}

	if reqID, hasReq := requestID(ctx); h.hasFunctionName || h.hasFunctionVersion || hasReq {
		buf.writeString(`,"record":{`)
		sep := false
		writeField := func(key, val string) {
			if sep {
				buf.writeByte(',')
			}
			appendJSONString(buf, key)
			buf.writeByte(':')
			appendJSONString(buf, val)
			sep = true
		}
		if h.hasFunctionName {
			writeField("functionName", h.functionName)
		}
		if h.hasFunctionVersion {
			writeField("version", h.functionVersion)
		}
		if hasReq {
			writeField("requestId", reqID)
		}
		buf.writeByte('}')
	}

	if logType := h.recordType(record); logType != "" {
		buf.writeString(`,"type":`)
		appendJSONString(buf, logType)
	}

	if h.source && record.PC != 0 {
		f := frameFor(record.PC)
		buf.writeString(`,"source":{"function":`)
		appendJSONString(buf, f.Function)
		buf.writeString(`,"file":`)
		appendJSONString(buf, f.File)
		buf.writeString(`,"line":`)
		*buf = strconv.AppendInt(*buf, int64(f.Line), 10)
		buf.writeByte('}')
	}

	// Attributes are nested under any open groups. Group braces are opened
	// lazily so that empty groups are omitted entirely.
	st := jsonState{buf: buf, needComma: true}
	atTop := true
	for _, ga := range h.gattr {
		if ga.group != "" {
			st.pending = append(st.pending, ga.group)
			atTop = false
		} else {
			for _, a := range ga.attrs {
				if atTop && isReservedType(a) {
					continue
				}
				st.appendAttr(a)
			}
		}
	}
	record.Attrs(func(a slog.Attr) bool {
		if atTop && isReservedType(a) {
			return true
		}
		st.appendAttr(a)
		return true
	})
	st.closeGroups()

	buf.writeString("}\n")
}

// jsonState tracks group nesting while streaming a record's attributes to a
// JSON buffer. Group objects are opened lazily (on the first attribute that
// lands inside them) so empty groups produce no output.
type jsonState struct {
	buf       *buffer
	pending   []string // group names not yet written as objects
	openDepth int      // number of group objects currently open
	needComma bool     // whether the next value needs a leading comma
}

func (s *jsonState) openPending() {
	for _, name := range s.pending {
		if s.needComma {
			s.buf.writeByte(',')
		}
		appendJSONString(s.buf, name)
		s.buf.writeByte(':')
		s.buf.writeByte('{')
		s.needComma = false
		s.openDepth++
	}
	s.pending = s.pending[:0]
}

func (s *jsonState) appendAttr(a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}

	if a.Value.Kind() == slog.KindGroup {
		attrs := a.Value.Group()
		if len(attrs) == 0 {
			return
		}
		if a.Key == "" {
			// An inline (unnamed) group hoists its attributes to the parent.
			for _, sub := range attrs {
				s.appendAttr(sub)
			}
			return
		}
		s.openPending()
		if s.needComma {
			s.buf.writeByte(',')
		}
		appendJSONString(s.buf, a.Key)
		s.buf.writeByte(':')
		s.buf.writeByte('{')
		s.needComma = false
		for _, sub := range attrs {
			s.appendAttr(sub)
		}
		s.buf.writeByte('}')
		s.needComma = true
		return
	}

	s.openPending()
	if s.needComma {
		s.buf.writeByte(',')
	}
	appendJSONString(s.buf, a.Key)
	s.buf.writeByte(':')
	appendJSONValue(s.buf, a.Value)
	s.needComma = true
}

func (s *jsonState) closeGroups() {
	for ; s.openDepth > 0; s.openDepth-- {
		s.buf.writeByte('}')
	}
}

func appendJSONValue(b *buffer, v slog.Value) {
	switch v.Kind() {
	case slog.KindString:
		appendJSONString(b, v.String())
	case slog.KindInt64:
		*b = strconv.AppendInt(*b, v.Int64(), 10)
	case slog.KindUint64:
		*b = strconv.AppendUint(*b, v.Uint64(), 10)
	case slog.KindFloat64:
		appendJSONFloat(b, v.Float64())
	case slog.KindBool:
		*b = strconv.AppendBool(*b, v.Bool())
	case slog.KindDuration:
		appendJSONString(b, v.Duration().String())
	case slog.KindTime:
		b.writeByte('"')
		*b = v.Time().AppendFormat(*b, time.RFC3339Nano)
		b.writeByte('"')
	case slog.KindAny:
		appendJSONAny(b, v.Any())
	default:
		// KindGroup is handled at the attribute level; anything else falls
		// back to its string form.
		appendJSONString(b, v.String())
	}
}

func appendJSONFloat(b *buffer, f float64) {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		// JSON has no representation for these, so emit a string instead of
		// producing invalid output.
		appendJSONString(b, strconv.FormatFloat(f, 'g', -1, 64))
		return
	}
	*b = strconv.AppendFloat(*b, f, 'g', -1, 64)
}

func appendJSONAny(b *buffer, v any) {
	switch x := v.(type) {
	case nil:
		b.writeString("null")
	case error:
		appendJSONString(b, x.Error())
	default:
		// Marshal arbitrary values (structs, slices, json.Marshalers, ...)
		// natively. This is the only reflection path and is rarely hit.
		raw, err := json.Marshal(v)
		if err != nil {
			appendJSONString(b, err.Error())
			return
		}
		*b = append(*b, raw...)
	}
}

// --- Text encoding -----------------------------------------------------------

func (h *Handler) appendText(buf *buffer, ctx context.Context, record slog.Record) {
	buf.writeString("level=")
	*buf = strconv.AppendQuote(*buf, lambdaLoggerLevelString(record.Level))
	buf.writeByte(' ')
	if record.Message != "" {
		buf.writeString("msg=")
		*buf = strconv.AppendQuote(*buf, record.Message)
		buf.writeByte(' ')
	}

	if !record.Time.IsZero() && !h.excludeTime {
		buf.writeString(`time="`)
		*buf = record.Time.AppendFormat(*buf, time.RFC3339Nano)
		buf.writeString(`" `)
	}

	if h.hasFunctionName {
		buf.writeString("record.functionName=")
		*buf = strconv.AppendQuote(*buf, h.functionName)
		buf.writeByte(' ')
	}
	if h.hasFunctionVersion {
		buf.writeString("record.version=")
		*buf = strconv.AppendQuote(*buf, h.functionVersion)
		buf.writeByte(' ')
	}
	if reqID, ok := requestID(ctx); ok {
		buf.writeString("record.requestId=")
		*buf = strconv.AppendQuote(*buf, reqID)
		buf.writeByte(' ')
	}

	if logType := h.recordType(record); logType != "" {
		buf.writeString("type=")
		*buf = strconv.AppendQuote(*buf, logType)
		buf.writeByte(' ')
	}

	if h.source && record.PC != 0 {
		f := frameFor(record.PC)
		buf.writeString("source.function=")
		*buf = strconv.AppendQuote(*buf, f.Function)
		buf.writeString(" source.file=")
		*buf = strconv.AppendQuote(*buf, f.File)
		buf.writeString(" source.line=")
		*buf = strconv.AppendInt(*buf, int64(f.Line), 10)
		buf.writeByte(' ')
	}

	// prefix accumulates open group names (dot-separated) and is grown in
	// place as groups open. Attributes are written with the prefix in effect
	// at their position in the chain.
	var prefix []byte
	atTop := true
	for _, ga := range h.gattr {
		if ga.group != "" {
			prefix = append(prefix, ga.group...)
			prefix = append(prefix, '.')
			atTop = false
		} else {
			for _, a := range ga.attrs {
				if atTop && isReservedType(a) {
					continue
				}
				appendTextAttr(buf, prefix, a)
			}
		}
	}
	record.Attrs(func(a slog.Attr) bool {
		if atTop && isReservedType(a) {
			return true
		}
		appendTextAttr(buf, prefix, a)
		return true
	})

	// Every field is written with a trailing space; replace the last one with
	// a newline. The level field guarantees the buffer is non-empty.
	if n := len(*buf); n > 0 && (*buf)[n-1] == ' ' {
		(*buf)[n-1] = '\n'
	} else {
		buf.writeByte('\n')
	}
}

func appendTextAttr(buf *buffer, prefix []byte, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}

	if a.Value.Kind() == slog.KindGroup {
		attrs := a.Value.Group()
		if len(attrs) == 0 {
			return
		}
		sub := prefix
		if a.Key != "" {
			// Build a fresh slice so growing the nested prefix never mutates
			// the caller's backing array.
			sub = append(append(append([]byte{}, prefix...), a.Key...), '.')
		}
		for _, s := range attrs {
			appendTextAttr(buf, sub, s)
		}
		return
	}

	*buf = append(*buf, prefix...)
	buf.writeString(a.Key)
	buf.writeByte('=')
	appendTextValue(buf, a.Value)
	buf.writeByte(' ')
}

func appendTextValue(b *buffer, v slog.Value) {
	switch v.Kind() {
	case slog.KindString:
		*b = strconv.AppendQuote(*b, v.String())
	case slog.KindInt64:
		*b = strconv.AppendInt(*b, v.Int64(), 10)
	case slog.KindUint64:
		*b = strconv.AppendUint(*b, v.Uint64(), 10)
	case slog.KindFloat64:
		*b = strconv.AppendFloat(*b, v.Float64(), 'g', -1, 64)
	case slog.KindBool:
		*b = strconv.AppendBool(*b, v.Bool())
	case slog.KindDuration:
		*b = strconv.AppendQuote(*b, v.Duration().String())
	case slog.KindTime:
		b.writeByte('"')
		*b = v.Time().AppendFormat(*b, time.RFC3339Nano)
		b.writeByte('"')
	case slog.KindAny:
		appendTextAny(b, v.Any())
	default:
		*b = strconv.AppendQuote(*b, v.String())
	}
}

func appendTextAny(b *buffer, v any) {
	switch x := v.(type) {
	case error:
		*b = strconv.AppendQuote(*b, x.Error())
	case json.Marshaler:
		raw, err := x.MarshalJSON()
		if err != nil {
			*b = strconv.AppendQuote(*b, err.Error())
			return
		}
		*b = strconv.AppendQuote(*b, string(raw))
	default:
		*b = fmt.Appendf(*b, "%v", v)
	}
}

// --- Shared helpers ----------------------------------------------------------

// recordType returns the effective "type" value for a record: the handler's
// configured type, overridden by a top-level (non-grouped) string attribute
// named TypeKey. A per-record attribute takes precedence over one set via
// WithAttrs. Attributes nested inside a group are not considered.
func (h *Handler) recordType(record slog.Record) string {
	logType := h.logType
	atTop := true
	for _, ga := range h.gattr {
		if ga.group != "" {
			atTop = false
			continue
		}
		if atTop {
			for _, a := range ga.attrs {
				if v, ok := reservedType(a); ok {
					logType = v
				}
			}
		}
	}
	if atTop {
		record.Attrs(func(a slog.Attr) bool {
			if v, ok := reservedType(a); ok {
				logType = v
			}
			return true
		})
	}
	return logType
}

// isReservedType reports whether a is the reserved TypeKey attribute, which is
// consumed to set the record's "type" field rather than emitted normally.
func isReservedType(a slog.Attr) bool {
	_, ok := reservedType(a)
	return ok
}

// reservedType returns the override type carried by a, if a is a string-valued
// TypeKey attribute.
func reservedType(a slog.Attr) (string, bool) {
	if a.Key != TypeKey {
		return "", false
	}
	if v := a.Value.Resolve(); v.Kind() == slog.KindString {
		return v.String(), true
	}
	return "", false
}

// requestID returns the AWS request ID from the invocation context, if present.
func requestID(ctx context.Context) (string, bool) {
	if lc, ok := voker.FromContext(ctx); ok && lc != nil {
		return lc.AwsRequestID, true
	}
	return "", false
}

func frameFor(pc uintptr) runtime.Frame {
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	return frame
}

const hexDigits = "0123456789abcdef"

// appendJSONString writes s as a JSON string literal, escaping control
// characters, quotes, and backslashes. '<', '>', and '&' are left unescaped,
// and valid UTF-8 is copied verbatim. Invalid UTF-8 bytes are replaced with
// U+FFFD, matching encoding/json, so the output is always valid JSON.
func appendJSONString(b *buffer, s string) {
	*b = append(*b, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' {
				i++
				continue
			}
			*b = append(*b, s[start:i]...)
			switch c {
			case '"':
				*b = append(*b, '\\', '"')
			case '\\':
				*b = append(*b, '\\', '\\')
			case '\n':
				*b = append(*b, '\\', 'n')
			case '\r':
				*b = append(*b, '\\', 'r')
			case '\t':
				*b = append(*b, '\\', 't')
			default:
				*b = append(*b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			*b = append(*b, s[start:i]...)
			*b = append(*b, `�`...)
			i++
			start = i
			continue
		}
		i += size
	}
	*b = append(*b, s[start:]...)
	*b = append(*b, '"')
}

// buffer is a reusable byte slice for assembling a single log record.
type buffer []byte

func (b *buffer) writeString(s string) { *b = append(*b, s...) }
func (b *buffer) writeByte(c byte)     { *b = append(*b, c) }

var bufferPool = sync.Pool{
	New: func() any {
		b := make(buffer, 0, 1024)
		return &b
	},
}

func newBuffer() *buffer {
	return bufferPool.Get().(*buffer)
}

func freeBuffer(b *buffer) {
	const maxBufferSize = 16 << 10

	if cap(*b) <= maxBufferSize {
		*b = (*b)[:0]
		bufferPool.Put(b)
	}
}
