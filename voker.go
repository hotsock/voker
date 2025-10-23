// Voker is a simplified alternative to aws-lambda-go that focuses on
// simplicity and modern Go idioms. It supports only a single handler
// signature using generics for type safety.
//
// Usage:
//
//	func handler(ctx context.Context, event MyEvent) (MyResponse, error) {
//	    // Handle the event
//	    return MyResponse{}, nil
//	}
//
//	func main() {
//	    voker.Start(handler)
//	}
package voker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

var errHandlerPanicked = errors.New("handler panicked")

type options struct {
	enableTraceID bool
	extensions    []InternalExtension
	logger        *slog.Logger
}

// Option is a function that modifies Options.
type Option func(*options)

// WithInternalExtension registers an internal extension.
func WithInternalExtension(ext InternalExtension) Option {
	return func(o *options) {
		o.extensions = append(o.extensions, ext)
	}
}

// WithLogger sets a custom slog logger for the runtime.
// If not provided, a default logger will be created based on
// AWS_LAMBDA_LOG_FORMAT and AWS_LAMBDA_LOG_LEVEL environment variables.
func WithLogger(logger *slog.Logger) Option {
	return func(o *options) {
		o.logger = logger
	}
}

// WithTraceID enables or disables AWS X-Ray tracing.
// When enabled, the X-Ray trace ID from Lambda headers will be
// set in the _X_AMZN_TRACE_ID environment variable for each invocation.
func WithTraceID(enabled bool) Option {
	return func(o *options) {
		o.enableTraceID = enabled
	}
}

// Start starts the Lambda runtime loop with the given handler function.
//
// The handler must have the signature:
//
//	func(context.Context, TIn) (TOut, error)
//
// Where TIn and TOut are JSON-serializable types.
//
// Options can be provided to configure runtime behavior:
//
//	voker.Start(handler, voker.WithTraceID(true))
//
// This function blocks indefinitely and only returns if a fatal error occurs.
func Start[TIn, TOut any](handler func(context.Context, TIn) (TOut, error), opts ...Option) {
	options := &options{}
	for _, opt := range opts {
		opt(options)
	}

	if options.logger == nil {
		options.logger = defaultLogger()
	}

	runtimeAPI := os.Getenv("AWS_LAMBDA_RUNTIME_API")
	if runtimeAPI == "" {
		options.logger.Error("AWS_LAMBDA_RUNTIME_API environment variable is not set")
		os.Exit(1)
	}

	done := make(chan struct{})

	if len(options.extensions) > 0 {
		extMgr := newExtensionManager(runtimeAPI, options.extensions, options.logger)
		if err := extMgr.start(); err != nil {
			options.logger.Error("failed to start extensions", "error", err)
			os.Exit(1)
		}

		sigterm := make(chan os.Signal, 1)
		signal.Notify(sigterm, syscall.SIGTERM)
		go func() {
			<-sigterm
			extMgr.shutdown()
			close(done)
		}()
	}

	client := newRuntimeClient(runtimeAPI, options.logger)

	for {
		select {
		case <-done:
			return
		default:
			if err := handleInvocation(client, handler, options); err != nil {
				// Don't log panics here - they're already logged in sendError
				if !errors.Is(err, errHandlerPanicked) {
					options.logger.Error("fatal invocation loop error", "error", err)
				}
				os.Exit(1)
			}
		}
	}
}

func handleInvocation[TIn, TOut any](client *runtimeClient, handler func(context.Context, TIn) (TOut, error), options *options) error {
	inv, err := client.next()
	if err != nil {
		return fmt.Errorf("failed to get next invocation: %w", err)
	}

	if options.enableTraceID {
		if traceID := inv.headers.Get(headerTraceID); traceID != "" {
			os.Setenv("_X_AMZN_TRACE_ID", traceID)
		}
	}

	deadline, err := parseDeadline(inv.headers.Get(headerDeadlineMS))
	if err != nil {
		return sendError(context.Background(), inv, newErrorResponse(err), options.logger)
	}

	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	lc := &LambdaContext{
		AwsRequestID:       inv.requestID,
		InvokedFunctionArn: inv.headers.Get(headerFunctionARN),
	}

	if cognitoJSON := inv.headers.Get(headerCognitoIdentity); cognitoJSON != "" {
		if err := json.Unmarshal([]byte(cognitoJSON), &lc.Identity); err != nil {
			return sendError(ctx, inv, newErrorResponse(fmt.Errorf("failed to parse cognito identity: %w", err)), options.logger)
		}
	}

	if clientJSON := inv.headers.Get(headerClientContext); clientJSON != "" {
		if err := json.Unmarshal([]byte(clientJSON), &lc.ClientContext); err != nil {
			return sendError(ctx, inv, newErrorResponse(fmt.Errorf("failed to parse client context: %w", err)), options.logger)
		}
	}

	ctx = NewContext(ctx, lc)

	response, err := callHandler(ctx, inv.payload, handler)
	if err != nil {
		return sendError(ctx, inv, err, options.logger)
	}

	if err := inv.success(response); err != nil {
		return fmt.Errorf("failed to send success response: %w", err)
	}

	return nil
}

func callHandler[TIn, TOut any](ctx context.Context, payload []byte, handler func(context.Context, TIn) (TOut, error)) (responseBytes []byte, responseErr error) {
	defer func() {
		if r := recover(); r != nil {
			responseBytes = nil
			responseErr = newPanicResponse(r)
		}
	}()

	var input TIn
	if err := json.Unmarshal(payload, &input); err != nil {
		return nil, &ErrorResponse{
			Message: fmt.Sprintf("failed to unmarshal input: %v", err),
			Type:    "Runtime.UnmarshalError",
		}
	}

	output, err := handler(ctx, input)
	if err != nil {
		return nil, newErrorResponse(err)
	}

	responseBytes, err = json.Marshal(output)
	if err != nil {
		return nil, &ErrorResponse{
			Message: fmt.Sprintf("failed to marshal output: %v", err),
			Type:    "Runtime.MarshalError",
		}
	}

	return responseBytes, nil
}

func sendError(ctx context.Context, inv *invocation, err error, logger *slog.Logger) error {
	var errResp *ErrorResponse

	if e, ok := err.(*ErrorResponse); ok {
		errResp = e
	} else {
		errResp = newErrorResponse(err)
	}

	errorJSON, marshalErr := json.Marshal(errResp)
	if marshalErr != nil {
		// If we can't marshal the error, create a simple error
		errorJSON = fmt.Appendf(nil, `{"Message":"failed to marshal error: %s","Type":"Runtime.MarshalError"}`, marshalErr.Error())
	}

	logger.ErrorContext(
		ctx,
		"invocation error",
		"error", errResp,
		slog.Group("record",
			"requestId", inv.requestID,
			"functionName", os.Getenv("AWS_LAMBDA_FUNCTION_NAME"),
			"functionVersion", os.Getenv("AWS_LAMBDA_FUNCTION_VERSION"),
		),
	)

	if err := inv.failure(errorJSON); err != nil {
		return fmt.Errorf("failed to send error response: %w", err)
	}

	if len(errResp.StackTrace) > 0 {
		return errHandlerPanicked
	}

	return nil
}

func parseDeadline(deadlineMS string) (time.Time, error) {
	ms, err := strconv.ParseInt(deadlineMS, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse deadline: %w", err)
	}
	return time.UnixMilli(ms), nil
}
