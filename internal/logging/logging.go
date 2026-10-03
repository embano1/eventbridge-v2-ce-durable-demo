// Package logging matches the durable SDK's default JSON field names, so one Logs Insights query
// covers the workflow and the trail.
package logging

import (
	"io"
	"log/slog"
	"time"
)

func Handler(w io.Writer, component string) slog.Handler {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) > 0 {
				return a
			}
			switch a.Key {
			case slog.TimeKey:
				return slog.String("timestamp", a.Value.Time().UTC().Format(time.RFC3339Nano))
			case slog.MessageKey:
				a.Key = "message"
			}
			return a
		},
	})
	return h.WithAttrs([]slog.Attr{slog.String("component", component)})
}

func New(w io.Writer, component string) *slog.Logger { return slog.New(Handler(w, component)) }
