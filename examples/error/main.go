package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/hotsock/voker"
)

func main() {
	voker.Start(handlerWithLambdaLogging(handler))
}

func handler(ctx context.Context, event any) (any, error) {
	slog.DebugContext(ctx, "debug message", "event", event)
	slog.InfoContext(ctx, "info message", "event", event)
	slog.WarnContext(ctx, "warn message", "event", event)
	slog.ErrorContext(ctx, "error message", "event", event)

	a := []string{"hey"}
	fmt.Println(a[1]) // panic

	return nil, nil
}

func handlerWithLambdaLogging[E, R any](handler func(context.Context, E) (R, error)) func(context.Context, E) (R, error) {
	var level slog.Level
	switch os.Getenv("AWS_LAMBDA_LOG_LEVEL") {
	case "DEBUG":
		level = slog.LevelDebug
	case "INFO":
		level = slog.LevelInfo
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	return func(ctx context.Context, event E) (R, error) {
		lc, _ := voker.FromContext(ctx)
		logHandler := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})).With("requestId", lc.AwsRequestID)
		slog.SetDefault(logHandler)

		return handler(ctx, event)
	}
}
