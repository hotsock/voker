package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/hotsock/voker"
)

const deadlineBuffer = 500 * time.Millisecond

var (
	activeInvocations atomic.Int64
	peakInvocations   atomic.Int64
	environmentID     = executionEnvironmentID()
)

type Event struct {
	ID            string `json:"id"`
	DelayMS       int64  `json:"delayMs"`
	Fail          bool   `json:"fail,omitempty"`
	DeadlineProbe bool   `json:"deadlineProbe,omitempty"`
}

type Response struct {
	ID                 string `json:"id"`
	RequestID          string `json:"requestId"`
	TraceID            string `json:"traceId"`
	EnvironmentID      string `json:"environmentId"`
	InitializationType string `json:"initializationType"`
	Architecture       string `json:"architecture"`
	MaxConcurrency     int    `json:"maxConcurrency"`
	ActiveAtStart      int64  `json:"activeAtStart"`
	PeakConcurrency    int64  `json:"peakConcurrency"`
}

func handler(ctx context.Context, event Event) (Response, error) {
	if event.ID == "" {
		return Response{}, &voker.ErrorResponse{
			Type:    "Application.ValidationError",
			Message: "id is required",
		}
	}
	if event.DelayMS < 0 {
		return Response{}, &voker.ErrorResponse{
			Type:    "Application.ValidationError",
			Message: "delayMs must not be negative",
		}
	}

	active := activeInvocations.Add(1)
	defer activeInvocations.Add(-1)
	updatePeak(active)

	workDuration := time.Duration(event.DelayMS) * time.Millisecond
	if deadline, ok := ctx.Deadline(); event.DeadlineProbe && ok {
		// Force this probe past the Lambda deadline so the guard path is
		// deterministic even when the function timeout changes.
		minimumDuration := time.Until(deadline) + time.Second
		if workDuration < minimumDuration {
			workDuration = minimumDuration
		}
	}
	workTimer := time.NewTimer(workDuration)
	defer workTimer.Stop()

	var deadlineGuard <-chan time.Time
	var guardTimer *time.Timer
	if deadline, ok := ctx.Deadline(); ok {
		guardAfter := time.Until(deadline.Add(-deadlineBuffer))
		if guardAfter < 0 {
			guardAfter = 0
		}
		guardTimer = time.NewTimer(guardAfter)
		defer guardTimer.Stop()
		deadlineGuard = guardTimer.C
	}

	select {
	case <-workTimer.C:
	case <-deadlineGuard:
		return Response{}, &voker.ErrorResponse{
			Type:    "Application.DeadlineGuard",
			Message: fmt.Sprintf("event %s stopped before the Lambda deadline", event.ID),
		}
	case <-ctx.Done():
		return Response{}, &voker.ErrorResponse{
			Type:    "Application.ContextCanceled",
			Message: fmt.Sprintf("event %s canceled: %v", event.ID, ctx.Err()),
		}
	}

	if event.Fail {
		return Response{}, &voker.ErrorResponse{
			Type:    "Application.ExpectedFailure",
			Message: fmt.Sprintf("event %s requested a failure", event.ID),
		}
	}

	lambdaContext, ok := voker.FromContext(ctx)
	if !ok {
		return Response{}, &voker.ErrorResponse{
			Type:    "Runtime.MissingContext",
			Message: "Lambda context is missing",
		}
	}

	return Response{
		ID:                 event.ID,
		RequestID:          lambdaContext.AwsRequestID,
		TraceID:            lambdaContext.TraceID,
		EnvironmentID:      environmentID,
		InitializationType: os.Getenv("AWS_LAMBDA_INITIALIZATION_TYPE"),
		Architecture:       runtime.GOARCH,
		MaxConcurrency:     voker.MaxConcurrency(),
		ActiveAtStart:      active,
		PeakConcurrency:    peakInvocations.Load(),
	}, nil
}

func updatePeak(value int64) {
	for current := peakInvocations.Load(); value > current; current = peakInvocations.Load() {
		if peakInvocations.CompareAndSwap(current, value) {
			return
		}
	}
}

func executionEnvironmentID() string {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown-host"
	}
	return fmt.Sprintf("%s-%d", hostname, os.Getpid())
}

func main() {
	voker.Start(handler)
}
