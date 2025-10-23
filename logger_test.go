package voker

import (
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoggerLevelFromString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected slog.Level
	}{
		{
			name:     "trace level",
			input:    "trace",
			expected: slog.LevelDebug - traceLevelDebugOffset,
		},
		{
			name:     "debug level",
			input:    "debug",
			expected: slog.LevelDebug,
		},
		{
			name:     "info level",
			input:    "info",
			expected: slog.LevelInfo,
		},
		{
			name:     "warn level",
			input:    "warn",
			expected: slog.LevelWarn,
		},
		{
			name:     "error level",
			input:    "error",
			expected: slog.LevelError,
		},
		{
			name:     "fatal level",
			input:    "fatal",
			expected: slog.LevelError + fatalLevelErrorOffset,
		},
		{
			name:     "uppercase",
			input:    "ERROR",
			expected: slog.LevelError,
		},
		{
			name:     "mixed case",
			input:    "WaRn",
			expected: slog.LevelWarn,
		},
		{
			name:     "with whitespace",
			input:    "  debug  ",
			expected: slog.LevelDebug,
		},
		{
			name:     "invalid level defaults to info",
			input:    "invalid",
			expected: slog.LevelInfo,
		},
		{
			name:     "empty string defaults to info",
			input:    "",
			expected: slog.LevelInfo,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := loggerLevelFromString(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestLoggerLevelFromLambdaEnv(t *testing.T) {
	original := os.Getenv(lambdaEnvLogLevel)
	defer os.Setenv(lambdaEnvLogLevel, original)

	tests := []struct {
		name     string
		envValue string
		expected slog.Level
	}{
		{
			name:     "error level from env",
			envValue: "error",
			expected: slog.LevelError,
		},
		{
			name:     "debug level from env",
			envValue: "debug",
			expected: slog.LevelDebug,
		},
		{
			name:     "unset env defaults to info",
			envValue: "",
			expected: slog.LevelInfo,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue == "" {
				os.Unsetenv(lambdaEnvLogLevel)
			} else {
				os.Setenv(lambdaEnvLogLevel, tt.envValue)
			}

			result := loggerLevelFromLambdaEnv()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDefaultLogger_Format(t *testing.T) {
	originalLevel := os.Getenv(lambdaEnvLogLevel)
	originalFormat := os.Getenv(lambdaEnvLogFormat)
	defer func() {
		os.Setenv(lambdaEnvLogLevel, originalLevel)
		os.Setenv(lambdaEnvLogFormat, originalFormat)
	}()

	tests := []struct {
		name        string
		logLevel    string
		logFormat   string
		description string
	}{
		{
			name:        "JSON format with error level",
			logLevel:    "error",
			logFormat:   "JSON",
			description: "Should create JSON handler with error level",
		},
		{
			name:        "text format with debug level",
			logLevel:    "debug",
			logFormat:   "text",
			description: "Should create text handler with debug level",
		},
		{
			name:        "default format with default level",
			logLevel:    "",
			logFormat:   "",
			description: "Should create text handler with info level",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.logLevel == "" {
				os.Unsetenv(lambdaEnvLogLevel)
			} else {
				os.Setenv(lambdaEnvLogLevel, tt.logLevel)
			}

			if tt.logFormat == "" {
				os.Unsetenv(lambdaEnvLogFormat)
			} else {
				os.Setenv(lambdaEnvLogFormat, tt.logFormat)
			}

			logger := defaultLogger()
			assert.NotNil(t, logger, tt.description)
		})
	}
}

func TestWithLogger(t *testing.T) {
	customLogger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	opts := &options{}

	WithLogger(customLogger)(opts)

	assert.Equal(t, customLogger, opts.logger)
}
