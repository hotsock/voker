package main

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/hotsock/voker"
)

func testContext(t *testing.T, timeout time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	return voker.NewContext(ctx, &voker.LambdaContext{
		AwsRequestID: "request-test",
		TraceID:      "trace-test",
	})
}

func TestHandlerSuccess(t *testing.T) {
	response, err := handler(testContext(t, time.Second), Event{ID: "success", DelayMS: 1})
	if err != nil {
		t.Fatalf("handler returned an error: %v", err)
	}
	if response.ID != "success" || response.RequestID != "request-test" || response.TraceID != "trace-test" || response.Architecture != runtime.GOARCH {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestHandlerExpectedFailure(t *testing.T) {
	_, err := handler(testContext(t, time.Second), Event{ID: "failure", Fail: true})
	var response *voker.ErrorResponse
	if !errors.As(err, &response) || response.Type != "Application.ExpectedFailure" {
		t.Fatalf("expected typed failure, got %v", err)
	}
}

func TestHandlerDeadlineGuard(t *testing.T) {
	_, err := handler(testContext(t, 50*time.Millisecond), Event{ID: "deadline", DelayMS: 1_000, DeadlineProbe: true})
	var response *voker.ErrorResponse
	if !errors.As(err, &response) || response.Type != "Application.DeadlineGuard" {
		t.Fatalf("expected deadline guard, got %v", err)
	}
}
