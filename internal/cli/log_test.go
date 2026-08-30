package cli

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
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
		{"属性付き", func(l *slog.Logger) { l.Info("exec", "command", "incus list") }, "command=incus list"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			tt.log(newLogger(&buf, false))

			if !strings.Contains(buf.String(), tt.want) {
				t.Errorf("output = %q, %q を含むこと", buf.String(), tt.want)
			}
		})
	}
}

func TestLoggerLevel(t *testing.T) {
	t.Run("通常はdebugを出さない", func(t *testing.T) {
		var buf bytes.Buffer
		newLogger(&buf, false).Debug("detail")

		if buf.Len() != 0 {
			t.Errorf("output = %q, want empty", buf.String())
		}
	})

	t.Run("verboseでdebugを出す", func(t *testing.T) {
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
			t.Errorf("output = %q, %q を含むこと", out, want)
		}
	}
}

// グループは出力形式を変えない（この実装では属性として平坦に扱う）
func TestLoggerWithGroup(t *testing.T) {
	var buf bytes.Buffer

	newLogger(&buf, false).WithGroup("incus").Info("started")

	if !strings.Contains(buf.String(), "[idev] started") {
		t.Errorf("output = %q", buf.String())
	}
}

func TestLoggerWriteError(t *testing.T) {
	// 書き込みに失敗してもpanicしないこと
	newLogger(errWriter{}, false).Info("message")
}
