package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

type contextKeyRequestID struct{}

// WithRequestID adds a Request ID to the given context.
func WithRequestID(ctx context.Context, reqID string) context.Context {
	return context.WithValue(ctx, contextKeyRequestID{}, reqID)
}

// GetRequestID retrieves the Request ID from the context, or returns empty.
func GetRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if reqID, ok := ctx.Value(contextKeyRequestID{}).(string); ok {
		return reqID
	}
	return ""
}

// ContextHandler wraps slog.Handler to inject context-bound attributes (like request_id) into log records.
type ContextHandler struct {
	slog.Handler
}

// Handle intercepts records and adds request_id to attributes if present.
func (h *ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if ctx != nil {
		if reqID := GetRequestID(ctx); reqID != "" {
			r.AddAttrs(slog.String("request_id", reqID))
		}
	}
	return h.Handler.Handle(ctx, r)
}

// Init initializes the global structured logger with the specified log level.
// It sets up a JSON handler outputting to standard output wrapped in our ContextHandler.
func Init(levelStr string) {
	var level slog.Level
	switch strings.ToLower(strings.TrimSpace(levelStr)) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})

	logger := slog.New(&ContextHandler{Handler: handler})
	slog.SetDefault(logger)
}
