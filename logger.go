package voker

import (
	"log/slog"
	"os"
	"strings"
)

const (
	lambdaEnvLogLevel  = "AWS_LAMBDA_LOG_LEVEL"
	lambdaEnvLogFormat = "AWS_LAMBDA_LOG_FORMAT"

	// traceLevelDebugOffset is the offset from slog.LevelDebug for TRACE level
	traceLevelDebugOffset = 4

	// fatalLevelErrorOffset is the offset from slog.LevelError for FATAL level
	fatalLevelErrorOffset = 4
)

// defaultLogger creates a logger based on AWS Lambda environment variables.
// AWS_LAMBDA_LOG_FORMAT controls output format (JSON or text).
// AWS_LAMBDA_LOG_LEVEL controls minimum log level (defaults to INFO).
//
// Note: Voker's internal logs only emit ERROR level messages. The log level
// setting allows filtering of these messages or logs from user code that
// uses the same logger instance.
func defaultLogger() *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: loggerLevelFromLambdaEnv(),
	}

	var handler slog.Handler
	if os.Getenv(lambdaEnvLogFormat) == "JSON" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}

	return slog.New(handler)
}

func loggerLevelFromLambdaEnv() slog.Level {
	return loggerLevelFromString(os.Getenv(lambdaEnvLogLevel))
}

// Supports: trace, debug, info, warn, error, fatal.
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
