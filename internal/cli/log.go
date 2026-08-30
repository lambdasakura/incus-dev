package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// handler は "[idev] メッセージ" 形式で出力する slog.Handler。
type handler struct {
	w     io.Writer
	level slog.Level
	attrs []slog.Attr
}

func newLogger(w io.Writer, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(&handler{w: w, level: level})
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	var sb strings.Builder
	sb.WriteString("[idev] ")

	switch r.Level {
	case slog.LevelWarn:
		sb.WriteString("warning: ")
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
