package voker

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewErrorResponse(t *testing.T) {
	err := errors.New("test error")
	errResp := newErrorResponse(err)

	assert.Equal(t, "test error", errResp.Message)
	assert.Equal(t, "Runtime.HandlerError", errResp.Type)
	assert.Empty(t, errResp.StackTrace)
}

type customError struct {
	msg string
}

func (e customError) Error() string {
	return e.msg
}

func TestNewErrorResponse_CustomType(t *testing.T) {
	err := customError{msg: "custom error"}
	errResp := newErrorResponse(err)

	assert.Equal(t, "custom error", errResp.Message)
	assert.Equal(t, "Runtime.customError", errResp.Type)
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
