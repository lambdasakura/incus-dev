package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// handler is a slog.Handler that prints "[idev] message".
type handler struct {
	w     io.Writer
	level slog.Level
	attrs []slog.Attr
	// warnings counts what has been reported, so a run that ends with several
	// does not sign off as though nothing happened. Shared, since WithAttrs
	// hands out a new handler over the same writer.
	warnings *int
}

func newLogger(w io.Writer, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(&handler{w: w, level: level, warnings: new(int)})
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	var sb strings.Builder
	sb.WriteString("[idev] ")

	switch r.Level {
	case slog.LevelWarn:
		sb.WriteString("warning: ")
		// A handler built without newLogger still logs; only the count is
		// unavailable to it.
		if h.warnings != nil {
			*h.warnings++
		}
	case slog.LevelError:
		sb.WriteString("error: ")
	}
	sb.WriteString(r.Message)

	for _, a := range h.attrs {
		fmt.Fprintf(&sb, " %s=%v", a.Key, a.Value)
	}
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&sb, " %s=%v", a.Key, a.Value)
		return true
	})

	sb.WriteByte('\n')
	_, err := io.WriteString(h.w, sb.String())
	return err
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &next
}

func (h *handler) WithGroup(string) slog.Handler { return h }

// warningCount returns how many warnings the logger has reported.
func warningCount(l *slog.Logger) int {
	h, ok := l.Handler().(*handler)
	if !ok || h.warnings == nil {
		return 0
	}
	return *h.warnings
}
