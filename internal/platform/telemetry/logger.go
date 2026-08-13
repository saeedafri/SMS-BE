// Package telemetry builds the process-wide structured logger. Output is JSON
// so production logs are machine-readable from the first commit rather than
// after someone regrets plain text.
package telemetry

import (
	"log/slog"
	"os"
)

func NewLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
