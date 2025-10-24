package voker

import (
	"fmt"
	"log/slog"
	"reflect"
	"runtime"
	"strings"
)

// ErrorResponse represents a Lambda function error response
type ErrorResponse struct {
	Type       string       `json:"errorType"`
	Message    string       `json:"errorMessage"`
	StackTrace []StackFrame `json:"stackTrace,omitempty"`
}

// Error implements the error interface for ErrorResponse
func (e *ErrorResponse) Error() string {
	return e.Message
}

// LogValue implements the slog.LogValuer interface for structured logging
func (e *ErrorResponse) LogValue() slog.Value {
	attrs := []slog.Attr{
		slog.String("errorType", e.Type),
		slog.String("errorMessage", e.Message),
	}

	if len(e.StackTrace) > 0 {
		frameValues := make([]any, len(e.StackTrace))
		for i, frame := range e.StackTrace {
			frameValues[i] = map[string]any{
				"path":  frame.Path,
				"line":  frame.Line,
				"label": frame.Label,
			}
		}
		attrs = append(attrs, slog.Any("stackTrace", frameValues))
	}

	return slog.GroupValue(attrs...)
}

// StackFrame represents a single frame in a stack trace
type StackFrame struct {
	Path  string `json:"path"`
	Line  int    `json:"line"`
	Label string `json:"label"`
}

// newErrorResponse creates an ErrorResponse from a regular error
func newErrorResponse(err error) *ErrorResponse {
	errorType := getErrorType(err)

	return &ErrorResponse{
		Message: err.Error(),
		Type:    errorType,
	}
}

// getErrorType returns the error type in AWS recommended format: Category.Reason
func getErrorType(err error) string {
	if err == nil {
		return "Runtime.Unknown"
	}

	t := reflect.TypeOf(err)
	if t == nil {
		return "Runtime.Unknown"
	}

	// Get the base type name
	typeName := t.Name()
	if t.Kind() == reflect.Pointer {
		typeName = t.Elem().Name()
	}

	// If we have a named type, use it with Runtime prefix
	if typeName != "" {
		// Handle standard library error types
		if typeName == "errorString" || typeName == "errors" {
			return "Runtime.HandlerError"
		}
		// Handle wrapped errors (fmt.wrapError, etc.)
		if strings.Contains(typeName, "wrap") {
			return "Runtime.HandlerError"
		}
		return "Runtime." + typeName
	}

	// Fallback for anonymous error types
	return "Runtime.HandlerError"
}

// newPanicResponse creates an ErrorResponse from a panic
func newPanicResponse(panicValue any) *ErrorResponse {
	message := fmt.Sprintf("%v", panicValue)
	errorType := getPanicType(panicValue)

	return &ErrorResponse{
		Message:    message,
		Type:       errorType,
		StackTrace: captureStackTrace(),
	}
}

// getPanicType returns the panic type in AWS recommended format
func getPanicType(panicValue any) string {
	if panicValue == nil {
		return "Runtime.Panic"
	}

	t := reflect.TypeOf(panicValue)
	typeName := t.Name()
	if t.Kind() == reflect.Pointer && t.Elem().Name() != "" {
		typeName = t.Elem().Name()
	}

	// If we have a type name, use it
	if typeName != "" {
		return "Runtime.Panic." + typeName
	}

	// For anonymous types, use the type string
	typeStr := fmt.Sprintf("%T", panicValue)
	// Clean up the type string (remove package paths)
	if idx := strings.LastIndex(typeStr, "."); idx >= 0 {
		typeStr = typeStr[idx+1:]
	}
	if typeStr != "" {
		return "Runtime.Panic." + typeStr
	}

	return "Runtime.Panic"
}

// captureStackTrace captures the current stack trace, skipping voker internal frames
func captureStackTrace() []StackFrame {
	const maxFrames = 32
	const framesToSkip = 4 // captureStackTrace -> newPanicResponse -> recover -> handler

	pcs := make([]uintptr, maxFrames)
	n := runtime.Callers(framesToSkip, pcs)
	if n == 0 {
		return []StackFrame{}
	}

	frames := runtime.CallersFrames(pcs[:n])
	var stackFrames []StackFrame

	for {
		frame, more := frames.Next()
		stackFrames = append(stackFrames, formatFrame(frame))
		if !more {
			break
		}
	}

	return stackFrames
}

// formatFrame converts a runtime.Frame to a StackFrame
func formatFrame(frame runtime.Frame) StackFrame {
	path := frame.File
	label := frame.Function

	// Strip GOPATH/module path from file path
	// Count slashes in function name to determine how many path components to keep
	slashCount := strings.Count(label, "/")
	if slashCount > 0 {
		parts := strings.Split(path, "/")
		if len(parts) > slashCount+1 {
			path = strings.Join(parts[len(parts)-slashCount-1:], "/")
		}
	}

	// Strip package path from function name
	if idx := strings.LastIndex(label, "/"); idx >= 0 {
		label = label[idx+1:]
	}
	// Strip package name, keeping only type and method
	if idx := strings.Index(label, "."); idx >= 0 {
		label = label[idx+1:]
	}

	return StackFrame{
		Path:  path,
		Line:  frame.Line,
		Label: label,
	}
}
