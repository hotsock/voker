package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/hotsock/voker"
	"github.com/hotsock/voker/vokerslog"
)

func main() {
	logger := slog.New(vokerslog.NewHandler(os.Stdout))
	slog.SetDefault(logger)

	voker.Start(handler, voker.WithLogger(logger))
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
