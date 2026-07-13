package voker

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewErrorResponse(t *testing.T) {
	err := errors.New("test error")
	errResp := newErrorResponse(err)

	assert.Equal(t, "test error", errResp.Message)
	assert.Equal(t, "HandlerError", errResp.Type)
	assert.Empty(t, errResp.StackTrace)
}

type customError struct {
	msg string
}

func (e customError) Error() string {
	return e.msg
}

type customPointerError struct {
	msg string
}

func (e *customPointerError) Error() string {
	return e.msg
}

func TestNewErrorResponse_CustomType(t *testing.T) {
	err := customError{msg: "custom error"}
	errResp := newErrorResponse(err)

	assert.Equal(t, "custom error", errResp.Message)
	assert.Equal(t, "customError", errResp.Type)
}

func TestNewErrorResponse_CustomPointerType(t *testing.T) {
	errResp := newErrorResponse(&customPointerError{msg: "custom pointer error"})

	assert.Equal(t, "custom pointer error", errResp.Message)
	assert.Equal(t, "customPointerError", errResp.Type)
}

func TestNewErrorResponse_PreservesErrorResponse(t *testing.T) {
	want := &ErrorResponse{
		Type:       "Application.ValidationError",
		Message:    "invalid input",
		StackTrace: []StackFrame{{Path: "handler.go", Line: 42, Label: "handler"}},
	}

	assert.Same(t, want, newErrorResponse(want))
}

func TestNewErrorResponse_PreservesWrappedErrorResponse(t *testing.T) {
	inner := &ErrorResponse{
		Type:    "Application.ValidationError",
		Message: "invalid input",
	}
	wrapped := fmt.Errorf("handler failed: %w", inner)

	assert.Same(t, inner, newErrorResponse(wrapped))
}

func TestGetErrorType(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "HandlerError"},
		{"errors.New", errors.New("boom"), "HandlerError"},
		{"fmt.Errorf", fmt.Errorf("boom"), "HandlerError"},
		{"fmt.Errorf wrapping", fmt.Errorf("wrapped: %w", errors.New("boom")), "HandlerError"},
		{"errors.Join", errors.Join(errors.New("a"), errors.New("b")), "HandlerError"},
		{"named value type", customError{msg: "boom"}, "customError"},
		{"named pointer type", &customPointerError{msg: "boom"}, "customPointerError"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, getErrorType(tt.err))
		})
	}
}

func TestNewPanicResponse(t *testing.T) {
	panicValue := "panic message"
	errResp := newPanicResponse(panicValue)

	assert.Equal(t, "panic message", errResp.Message)
	assert.Equal(t, "Runtime.Panic.string", errResp.Type)
	assert.NotEmpty(t, errResp.StackTrace)

	// Verify stack trace has reasonable structure
	for _, frame := range errResp.StackTrace {
		assert.NotEmpty(t, frame.Path)
		assert.Greater(t, frame.Line, 0)
		assert.NotEmpty(t, frame.Label)
	}
}

func TestNewPanicResponse_CustomType(t *testing.T) {
	panicValue := customError{msg: "panic error"}
	errResp := newPanicResponse(panicValue)

	assert.Equal(t, "panic error", errResp.Message)
	assert.Equal(t, "Runtime.Panic.customError", errResp.Type)
	assert.NotEmpty(t, errResp.StackTrace)
}

func TestGetPanicType(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"nil", nil, "Runtime.Panic"},
		{"string", "boom", "Runtime.Panic.string"},
		{"error value", customError{msg: "boom"}, "Runtime.Panic.customError"},
		{"pointer", &customPointerError{msg: "boom"}, "Runtime.Panic.customPointerError"},
		{"int", 42, "Runtime.Panic.int"},
		{"anonymous struct", struct{ X int }{X: 1}, "Runtime.Panic.struct { X int }"},
		{"pointer to anonymous struct", &struct{ X int }{X: 1}, "Runtime.Panic.*struct { X int }"},
		{"slice", []string{"boom"}, "Runtime.Panic.[]string"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, getPanicType(tt.value))
		})
	}
}

func TestCaptureStackTrace(t *testing.T) {
	frames := captureStackTrace()
	assert.NotEmpty(t, frames)

	// Should have at least one frame
	assert.Greater(t, len(frames), 0)

	// Frames should have valid data
	for _, frame := range frames {
		assert.NotEmpty(t, frame.Path)
		assert.Greater(t, frame.Line, 0)
		assert.NotEmpty(t, frame.Label)
	}
}
