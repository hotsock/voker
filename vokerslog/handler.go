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
	json "encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
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

	// pre is the WithAttrs/WithGroup chain, serialized once so Handle only
	// copies bytes.
	pre *preformatted
}

// Option configures a [Handler]. Pass options to [NewHandler].
type Option func(*Handler)

// WithLevel sets the minimum log level. Messages below this level are dropped.
// Defaults to the value of AWS_LAMBDA_LOG_LEVEL, or INFO if unset. A nil
// level means INFO, matching [slog.HandlerOptions].
func WithLevel(level slog.Leveler) Option {
	if level == nil {
		level = slog.LevelInfo
	}
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
		pre:     &preformatted{sep: true},
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

// WithAttrs returns a Handler that writes attrs on every record. Like
// [slog.JSONHandler], it serializes attrs once, here, so any [slog.LogValuer]
// among them is resolved once rather than on every record.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	c := *h
	c.preformatAttrs(attrs)
	return &c
}

func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		// slog requires WithGroup("") to be a no-op.
		return h
	}
	c := *h
	c.pre = h.pre.withGroup(name)
	return &c
}

// preformatted is the WithAttrs/WithGroup chain serialized once, the way
// slog.JSONHandler pre-formats its attrs. groups[:nOpen] are open in buf (and
// prefix, for text) and Handle closes them after the record's own attrs;
// groups[nOpen:] are still pending and only open if the record has attrs to
// put in them, so a trailing empty group is omitted.
type preformatted struct {
	buf         []byte
	sep         bool
	prefix      []byte
	prefixQuote bool
	groups      []string
	nOpen       int
}

func (p *preformatted) withGroup(name string) *preformatted {
	c := *p
	c.groups = append(p.groups[:len(p.groups):len(p.groups)], name)
	return &c
}

// preformatAttrs serializes attrs onto h.pre, opening any pending groups first
// and leaving them open for Handle to close. If attrs write nothing, h.pre and
// its pending groups are left as they were. At the top level (no groups), a
// TypeKey string attr replaces h.logType instead of being written.
func (h *Handler) preformatAttrs(attrs []slog.Attr) {
	p := h.pre
	s := &handleState{h: h, buf: make([]byte, 0, len(p.buf)+64)}
	s.loadPreformatted(p)
	for _, name := range p.groups[p.nOpen:] {
		s.openGroup(name)
	}
	start := len(s.buf)

	atTop := len(p.groups) == 0
	for _, a := range attrs {
		if atTop && a.Key == TypeKey {
			a.Value = a.Value.Resolve()
			if a.Value.Kind() == slog.KindString {
				h.logType = a.Value.String()
				continue
			}
		}
		s.appendAttr(a)
	}

	if len(s.buf) == start {
		return
	}
	h.pre = &preformatted{
		buf:         s.buf,
		sep:         s.sep,
		prefix:      s.prefix,
		prefixQuote: s.prefixQuote,
		groups:      p.groups,
		nOpen:       len(p.groups),
	}
}

func (h *Handler) Handle(ctx context.Context, record slog.Record) error {
	s := newHandleState(h)
	defer s.free()

	if h.json {
		s.buf = append(s.buf, '{')
	}

	s.appendBuiltinKey(slog.LevelKey)
	s.appendString(lambdaLoggerLevelString(record.Level))

	if record.Message != "" {
		s.appendBuiltinKey(slog.MessageKey)
		s.appendString(record.Message)
	}

	if !record.Time.IsZero() && !h.excludeTime {
		s.appendBuiltinKey(slog.TimeKey)
		s.appendTime(record.Time)
	}

	if reqID, hasReq := requestID(ctx); h.hasFunctionName || h.hasFunctionVersion || hasReq {
		s.openBuiltinGroup("record")
		if h.hasFunctionName {
			s.appendBuiltinKey("functionName")
			s.appendString(h.functionName)
		}
		if h.hasFunctionVersion {
			s.appendBuiltinKey("version")
			s.appendString(h.functionVersion)
		}
		if hasReq {
			s.appendBuiltinKey("requestId")
			s.appendString(reqID)
		}
		s.closeBuiltinGroup()
	}

	if logType := s.recordType(&record); logType != "" {
		s.appendBuiltinKey(TypeKey)
		s.appendString(logType)
	}

	if h.source && record.PC != 0 {
		f := frameFor(record.PC)
		s.openBuiltinGroup(slog.SourceKey)
		s.appendBuiltinKey("function")
		s.appendString(f.Function)
		s.appendBuiltinKey("file")
		s.appendString(f.File)
		s.appendBuiltinKey("line")
		s.buf = strconv.AppendInt(s.buf, int64(f.Line), 10)
		s.closeBuiltinGroup()
	}

	s.appendAttrs(&record)

	if h.json {
		s.buf = append(s.buf, '}')
	}
	s.buf = append(s.buf, '\n')

	h.mu.Lock()
	defer h.mu.Unlock()

	_, err := h.out.Write(s.buf)
	return err
}

var _ slog.Handler = (*Handler)(nil)

// --- Record encoding ---------------------------------------------------------

// handleState assembles one record directly into buf, in either format.
type handleState struct {
	h   *Handler
	buf []byte
	sep bool // whether the next key needs a leading separator

	// Text output flattens groups into a dotted key prefix.
	prefix      []byte
	prefixQuote bool

	marks      []groupMark  // pending groups opened by appendAttrs
	typeValues []slog.Value // resolved top-level TypeKey record attrs, in order
}

// groupMark records the state before openGroup, so closeGroup can drop the
// group again if nothing was written inside it.
type groupMark struct {
	start       int
	opened      int
	sep         bool
	prefixLen   int
	prefixQuote bool
}

// maxBufferSize caps the buffers kept in statePool, so one oversized record
// doesn't pin its memory for the life of the process.
const maxBufferSize = 16 << 10

var statePool = sync.Pool{
	New: func() any {
		return &handleState{buf: make([]byte, 0, 1024)}
	},
}

func newHandleState(h *Handler) *handleState {
	s := statePool.Get().(*handleState)
	s.h = h
	return s
}

func (s *handleState) free() {
	if cap(s.buf) > maxBufferSize || cap(s.prefix) > maxBufferSize {
		return
	}
	clear(s.typeValues)
	s.h = nil
	s.buf = s.buf[:0]
	s.sep = false
	s.prefix = s.prefix[:0]
	s.prefixQuote = false
	s.marks = s.marks[:0]
	s.typeValues = s.typeValues[:0]
	statePool.Put(s)
}

// Write appends p to the record, so json.MarshalWrite can encode into it.
func (s *handleState) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	return len(p), nil
}

func (s *handleState) loadPreformatted(p *preformatted) {
	s.buf = append(s.buf, p.buf...)
	s.sep = p.sep
	s.prefix = append(s.prefix[:0], p.prefix...)
	s.prefixQuote = p.prefixQuote
}

// recordType returns the effective "type" value for a record: the handler's
// type (from WithType or a TypeKey attr passed to WithAttrs), overridden by a
// top-level string attribute named TypeKey on the record. Attributes nested in
// a group are not considered. The resolved TypeKey values are kept for
// appendAttrs so a LogValuer is only resolved once.
func (s *handleState) recordType(r *slog.Record) string {
	logType := s.h.logType
	if len(s.h.pre.groups) > 0 {
		return logType
	}
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == TypeKey {
			v := a.Value.Resolve()
			s.typeValues = append(s.typeValues, v)
			if v.Kind() == slog.KindString {
				logType = v.String()
			}
		}
		return true
	})
	return logType
}

// appendAttrs writes the preformatted chain, then the record's attributes
// inside any groups the chain left open or pending. recordType must have run
// first.
func (s *handleState) appendAttrs(r *slog.Record) {
	p := s.h.pre
	s.loadPreformatted(p)
	for _, name := range p.groups[p.nOpen:] {
		s.marks = append(s.marks, s.openGroup(name))
	}

	atTop := len(p.groups) == 0
	n := 0
	r.Attrs(func(a slog.Attr) bool {
		if atTop && a.Key == TypeKey {
			a.Value = s.typeValues[n]
			n++
			if a.Value.Kind() == slog.KindString {
				return true
			}
		}
		s.appendAttr(a)
		return true
	})

	for i := len(s.marks) - 1; i >= 0; i-- {
		s.closeGroup(s.marks[i])
	}
	if s.h.json {
		for range p.nOpen {
			s.buf = append(s.buf, '}')
		}
	}
}

func (s *handleState) appendAttr(a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}

	if a.Value.Kind() != slog.KindGroup {
		s.appendKey(a.Key)
		s.appendValue(a.Value)
		return
	}

	if a.Key == "" {
		// An inline (unnamed) group hoists its attributes to the parent.
		for _, sub := range a.Value.Group() {
			s.appendAttr(sub)
		}
		return
	}

	g := s.openGroup(a.Key)
	for _, sub := range a.Value.Group() {
		s.appendAttr(sub)
	}
	s.closeGroup(g)
}

// openGroup starts a nested group: an object in JSON, a key prefix in text.
// closeGroup drops it again if nothing was written inside, since slog
// requires empty groups to be omitted.
func (s *handleState) openGroup(name string) groupMark {
	g := groupMark{
		start:       len(s.buf),
		sep:         s.sep,
		prefixLen:   len(s.prefix),
		prefixQuote: s.prefixQuote,
	}

	if s.h.json {
		s.appendKey(name)
		s.buf = append(s.buf, '{')
		s.sep = false
	} else {
		s.prefix = append(append(s.prefix, name...), '.')
		s.prefixQuote = s.prefixQuote || textNeedsQuoting(name)
	}

	g.opened = len(s.buf)
	return g
}

func (s *handleState) closeGroup(g groupMark) {
	s.prefix = s.prefix[:g.prefixLen]
	s.prefixQuote = g.prefixQuote

	if len(s.buf) == g.opened {
		s.buf = s.buf[:g.start]
		s.sep = g.sep
		return
	}

	if s.h.json {
		s.buf = append(s.buf, '}')
	}
}

func (s *handleState) appendKey(key string) {
	if s.sep {
		if s.h.json {
			s.buf = append(s.buf, ',')
		} else {
			s.buf = append(s.buf, ' ')
		}
	}
	s.sep = true

	if s.h.json {
		s.buf = appendJSONString(s.buf, key)
		s.buf = append(s.buf, ':')
		return
	}

	if s.prefixQuote || textNeedsQuoting(key) {
		s.buf = strconv.AppendQuote(s.buf, string(s.prefix)+key)
	} else {
		s.buf = append(s.buf, s.prefix...)
		s.buf = append(s.buf, key...)
	}
	s.buf = append(s.buf, '=')
}

// appendBuiltinKey is appendKey for the handler's own keys, which never need
// escaping or quoting.
func (s *handleState) appendBuiltinKey(key string) {
	if s.h.json {
		if s.sep {
			s.buf = append(s.buf, ',')
		}
		s.buf = append(s.buf, '"')
		s.buf = append(s.buf, key...)
		s.buf = append(s.buf, '"', ':')
	} else {
		if s.sep {
			s.buf = append(s.buf, ' ')
		}
		s.buf = append(s.buf, s.prefix...)
		s.buf = append(s.buf, key...)
		s.buf = append(s.buf, '=')
	}
	s.sep = true
}

// openBuiltinGroup and closeBuiltinGroup bracket one of the handler's own
// top-level groups ("record", "source"), which are never empty.
func (s *handleState) openBuiltinGroup(name string) {
	if s.h.json {
		s.appendBuiltinKey(name)
		s.buf = append(s.buf, '{')
		s.sep = false
	} else {
		s.prefix = append(append(s.prefix, name...), '.')
	}
}

func (s *handleState) closeBuiltinGroup() {
	if s.h.json {
		s.buf = append(s.buf, '}')
	} else {
		s.prefix = s.prefix[:0]
	}
}

func (s *handleState) appendValue(v slog.Value) {
	switch v.Kind() {
	case slog.KindString:
		s.appendString(v.String())
	case slog.KindInt64:
		s.buf = strconv.AppendInt(s.buf, v.Int64(), 10)
	case slog.KindUint64:
		s.buf = strconv.AppendUint(s.buf, v.Uint64(), 10)
	case slog.KindFloat64:
		s.appendFloat(v.Float64())
	case slog.KindBool:
		s.buf = strconv.AppendBool(s.buf, v.Bool())
	case slog.KindDuration:
		s.appendString(v.Duration().String())
	case slog.KindTime:
		s.appendTime(v.Time())
	case slog.KindAny:
		s.appendAny(v.Any())
	default:
		// KindGroup is handled at the attribute level; anything else falls
		// back to its string form.
		s.appendString(v.String())
	}
}

// appendString writes a JSON string, or an always-quoted text value.
func (s *handleState) appendString(str string) {
	if s.h.json {
		s.buf = appendJSONString(s.buf, str)
	} else {
		s.buf = strconv.AppendQuote(s.buf, str)
	}
}

func (s *handleState) appendTime(t time.Time) {
	s.buf = append(s.buf, '"')
	s.buf = t.AppendFormat(s.buf, time.RFC3339Nano)
	s.buf = append(s.buf, '"')
}

func (s *handleState) appendFloat(f float64) {
	if s.h.json && (math.IsInf(f, 0) || math.IsNaN(f)) {
		// JSON has no representation for these, so emit a string instead of
		// producing invalid output.
		s.appendString(strconv.FormatFloat(f, 'g', -1, 64))
		return
	}
	s.buf = strconv.AppendFloat(s.buf, f, 'g', -1, 64)
}

// appendAny writes a KindAny value. This is where user code runs (Error,
// MarshalJSON, String), so like slog's built-in handlers it recovers from a
// panic instead of taking down the invocation: a nil pointer receiver is
// written as "<nil>", anything else as the panic message.
func (s *handleState) appendAny(v any) {
	mark := len(s.buf)
	defer func() {
		if r := recover(); r != nil {
			s.buf = s.buf[:mark]
			if rv := reflect.ValueOf(v); rv.Kind() == reflect.Pointer && rv.IsNil() {
				s.appendString("<nil>")
				return
			}
			s.appendString(fmt.Sprintf("!PANIC: %v", r))
		}
	}()

	if s.h.json {
		s.appendJSONAny(v)
	} else {
		s.appendTextAny(v)
	}
}

func (s *handleState) appendJSONAny(v any) {
	switch x := v.(type) {
	case nil:
		s.buf = append(s.buf, "null"...)
	case error:
		s.buf = appendJSONString(s.buf, x.Error())
	default:
		// Marshal arbitrary values (structs, slices, json.Marshalers, ...)
		// natively, straight into the buffer. This is the only reflection path
		// and is rarely hit.
		mark := len(s.buf)
		if err := json.MarshalWrite(s, v); err != nil {
			s.buf = appendJSONString(s.buf[:mark], err.Error())
		}
	}
}

func (s *handleState) appendTextAny(v any) {
	switch x := v.(type) {
	case error:
		s.buf = strconv.AppendQuote(s.buf, x.Error())
	case json.Marshaler:
		raw, err := x.MarshalJSON()
		if err != nil {
			s.buf = strconv.AppendQuote(s.buf, err.Error())
			return
		}
		s.buf = strconv.AppendQuote(s.buf, string(raw))
	case json.MarshalerTo:
		raw, err := json.Marshal(x)
		if err != nil {
			s.buf = strconv.AppendQuote(s.buf, err.Error())
			return
		}
		s.buf = strconv.AppendQuote(s.buf, string(raw))
	default:
		mark := len(s.buf)
		s.buf = fmt.Appendf(s.buf, "%v", v)
		if str := string(s.buf[mark:]); textNeedsQuoting(str) {
			s.buf = strconv.AppendQuote(s.buf[:mark], str)
		}
	}
}

// --- Shared helpers ----------------------------------------------------------

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

// appendJSONString appends s as a JSON string literal, escaping control
// characters, quotes, and backslashes. '<', '>', and '&' are left unescaped,
// and valid UTF-8 is copied verbatim. Invalid UTF-8 bytes are replaced with
// U+FFFD, matching encoding/json, so the output is always valid JSON.
func appendJSONString(b []byte, s string) []byte {
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' {
				i++
				continue
			}
			b = append(b, s[start:i]...)
			switch c {
			case '"':
				b = append(b, '\\', '"')
			case '\\':
				b = append(b, '\\', '\\')
			case '\n':
				b = append(b, '\\', 'n')
			case '\r':
				b = append(b, '\\', 'r')
			case '\t':
				b = append(b, '\\', 't')
			default:
				b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, s[start:i]...)
			b = append(b, `�`...)
			i++
			start = i
			continue
		}
		i += size
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}

// textNeedsQuoting reports whether a text key or unquoted value must be quoted
// to keep the line parseable, using the same rules as [slog.TextHandler].
func textNeedsQuoting(s string) bool {
	if len(s) == 0 {
		return true
	}
	for i := 0; i < len(s); {
		b := s[i]
		if b < utf8.RuneSelf {
			if b == ' ' || b == '=' || b == '"' || b < 0x20 || b == 0x7f {
				return true
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError || unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return true
		}
		i += size
	}
	return false
}
