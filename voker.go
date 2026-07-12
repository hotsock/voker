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
	"io"
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

// WithTraceID enables or disables AWS X-Ray trace ID propagation. Propagation
// is disabled by default. When enabled, the trace ID from Lambda headers is set
// in _X_AMZN_TRACE_ID for each invocation.
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
// Where TIn is JSON-deserializable. TOut is normally JSON-serializable. When
// TOut implements io.Reader, voker instead streams it through the Lambda
// Runtime API. A streaming response can optionally implement this interface
// to control the content type propagated by Lambda:
//
//	type contentTypeResponse interface {
//	    io.Reader
//	    ContentType() string
//	}
//
// As a special case, a handler may declare TIn as json.RawMessage to receive
// the invocation payload verbatim. voker skips unmarshaling (and JSON
// validation) and hands the raw bytes to the handler, which is then
// responsible for decoding them. This is useful for handlers that work with
// large payloads and want to measure or control their own decoding.
//
// Options can be provided to configure runtime behavior:
//
//	voker.Start(handler, voker.WithTraceID(true))
//
// This function blocks indefinitely and only returns if a fatal error occurs.
func Start[TIn, TOut any](handler func(context.Context, TIn) (TOut, error), opts ...Option) {
	start(func(client *runtimeClient, options *options) error {
		return handleInvocation(client, handler, options)
	}, opts...)
}

func start(handle func(*runtimeClient, *options) error, opts ...Option) {
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
	client := newRuntimeClient(runtimeAPI, options.logger)

	if len(options.extensions) > 0 {
		extMgr := newExtensionManager(runtimeAPI, options.extensions, options.logger)
		if err := extMgr.start(); err != nil {
			options.logger.Error("failed to start extensions", "error", err)
			if reportErr := sendInitError(client, err); reportErr != nil {
				options.logger.Error("failed to report initialization error", "error", reportErr)
			}
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

	for {
		select {
		case <-done:
			return
		default:
			if err := handle(client, options); err != nil {
				// Don't log panics here - they're already logged in sendError
				if !errors.Is(err, errHandlerPanicked) {
					options.logger.Error("fatal invocation loop error", "error", err)
				}
				os.Exit(1)
			}
		}
	}
}

func sendInitError(client *runtimeClient, err error) error {
	errorJSON, marshalErr := json.Marshal(newErrorResponse(err))
	if marshalErr != nil {
		errorJSON = fmt.Appendf(nil, `{"errorMessage":"failed to marshal initialization error: %s","errorType":"Runtime.MarshalError"}`, marshalErr)
	}
	if postErr := client.initFailure(errorJSON); postErr != nil {
		return fmt.Errorf("failed to send initialization error: %w", postErr)
	}
	return nil
}

func handleInvocation[TIn, TOut any](client *runtimeClient, handler func(context.Context, TIn) (TOut, error), options *options) error {
	inv, err := client.next()
	if err != nil {
		return fmt.Errorf("failed to get next invocation: %w", err)
	}

	if options.enableTraceID {
		// Set this on every invocation, including an empty value, so a missing
		// header cannot leak the preceding invocation's trace ID.
		_ = os.Setenv("_X_AMZN_TRACE_ID", inv.headers.Get(headerTraceID))
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

	if response.stream != nil {
		streamErr, err := inv.successStreaming(ctx, response.stream, response.contentType)
		if err != nil {
			return fmt.Errorf("failed to send streaming response: %w", err)
		}
		if streamErr != nil {
			options.logger.ErrorContext(ctx, "streaming invocation error", "error", streamErr)
			if typed, ok := streamErr.(*ErrorResponse); ok && typed.fatal {
				return errHandlerPanicked
			}
		}
	} else if err := inv.success(response.payload); err != nil {
		return fmt.Errorf("failed to send success response: %w", err)
	}

	return nil
}

type handlerResponse struct {
	payload     []byte
	stream      io.Reader
	contentType string
}

func callHandler[TIn, TOut any](ctx context.Context, payload []byte, handler func(context.Context, TIn) (TOut, error)) (response handlerResponse, responseErr error) {
	defer func() {
		if r := recover(); r != nil {
			response = handlerResponse{}
			responseErr = newPanicResponse(r)
		}
	}()

	var input TIn

	// When the handler's input type is json.RawMessage, hand it the raw payload
	// verbatim and skip unmarshaling entirely. This lets handlers that work with
	// large payloads measure and control their own decoding rather than paying
	// for an unmarshal they didn't ask for.
	//
	// The payload is aliased, not copied: each invocation receives a fresh
	// buffer (see runtimeClient.next) that voker never reuses or mutates, so
	// the handler can safely read it for the duration of the invocation.
	//
	// Note: this also bypasses JSON validation. A json.RawMessage handler
	// receives the bytes as-is, even if the payload is empty or not valid JSON,
	// and is responsible for handling those cases itself.
	if raw, ok := any(&input).(*json.RawMessage); ok {
		*raw = payload
	} else if err := json.Unmarshal(payload, &input); err != nil {
		return handlerResponse{}, &ErrorResponse{
			Message: fmt.Sprintf("failed to unmarshal input: %v", err),
			Type:    "Runtime.UnmarshalError",
		}
	}

	output, err := handler(ctx, input)
	if err != nil {
		return handlerResponse{}, newErrorResponse(err)
	}

	if stream, ok := any(output).(io.Reader); ok {
		contentType := "application/octet-stream"
		if typed, ok := any(output).(interface{ ContentType() string }); ok {
			contentType = typed.ContentType()
		}
		return handlerResponse{stream: stream, contentType: contentType}, nil
	}

	responseBytes, err := json.Marshal(output)
	if err != nil {
		return handlerResponse{}, &ErrorResponse{
			Message: fmt.Sprintf("failed to marshal output: %v", err),
			Type:    "Runtime.MarshalError",
		}
	}

	return handlerResponse{payload: responseBytes}, nil
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
		errorJSON = fmt.Appendf(nil, `{"message":"failed to marshal error: %s","type":"Runtime.MarshalError"}`, marshalErr.Error())
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

	if errResp.fatal {
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
