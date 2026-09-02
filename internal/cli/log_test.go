package cli

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLoggerFormat(t *testing.T) {
	tests := []struct {
		name string
		log  func(*slog.Logger)
		want string
	}{
		{"info", func(l *slog.Logger) { l.Info("creating instance") }, "[idev] creating instance"},
		{"warn", func(l *slog.Logger) { l.Warn("falling back") }, "[idev] warning: falling back"},
		{"error", func(l *slog.Logger) { l.Error("failed") }, "[idev] error: failed"},
		{"with attributes", func(l *slog.Logger) { l.Info("exec", "command", "incus list") }, "command=incus list"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			tt.log(newLogger(&buf, false))

			if !strings.Contains(buf.String(), tt.want) {
				t.Errorf("output = %q, want it to contain %q", buf.String(), tt.want)
			}
		})
	}
}

func TestLoggerLevel(t *testing.T) {
	t.Run("debug is not printed by default", func(t *testing.T) {
		var buf bytes.Buffer
		newLogger(&buf, false).Debug("detail")

		if buf.Len() != 0 {
			t.Errorf("output = %q, want empty", buf.String())
		}
	})

	t.Run("verbose prints debug", func(t *testing.T) {
		var buf bytes.Buffer
		newLogger(&buf, true).Debug("detail", "key", "value")

		if !strings.Contains(buf.String(), "detail") || !strings.Contains(buf.String(), "key=value") {
			t.Errorf("output = %q", buf.String())
		}
	})
}

func TestLoggerWithAttrs(t *testing.T) {
	var buf bytes.Buffer

	newLogger(&buf, false).With("step", "provision").Info("running", "index", 2)

	out := buf.String()
	for _, want := range []string{"running", "step=provision", "index=2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want it to contain %q", out, want)
		}
	}
}

// A group does not change the output; this implementation flattens it into the
// attributes.
func TestLoggerWithGroup(t *testing.T) {
	var buf bytes.Buffer

	newLogger(&buf, false).WithGroup("incus").Info("started")

	if !strings.Contains(buf.String(), "[idev] started") {
		t.Errorf("output = %q", buf.String())
	}
}

// A failed write to the output does not stop the call that logged it.
func TestLoggerWriteError(t *testing.T) {
	h := &handler{w: errWriter{}, level: slog.LevelInfo}

	err := h.Handle(context.Background(), slog.NewRecord(time.Time{}, slog.LevelInfo, "message", 0))
	if err == nil {
		t.Error("Handle() = nil error, want the write failure returned")
	}

	// Through slog the error is dropped, and nothing panics.
	newLogger(errWriter{}, false).Info("message")
}
