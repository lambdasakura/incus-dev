package incus_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lambdasakura/incus-dev/internal/incus"
	"github.com/lambdasakura/incus-dev/internal/runner/runnertest"
)

// 利用者が指定した値はSecretを含みうるため、表示用文字列で隠す
// （仕様 04-cli.md 4.10）
func TestValuesAreRedactedInDisplay(t *testing.T) {
	tests := []struct {
		name   string
		call   func(*incus.CLI) error
		stdout string
		secret string
		want   string // 表示に残るべき文字列
	}{
		{
			name: "exec の環境変数",
			call: func(c *incus.CLI) error {
				_, err := c.Exec(context.Background(), "dev-x", []string{"true"}, incus.ExecOptions{
					Env: map[string]string{"TOKEN": "s3cret"},
				})
				return err
			},
			secret: "s3cret",
			want:   "TOKEN=***",
		},
		{
			name: "config set の値",
			call: func(c *incus.CLI) error {
				return c.ApplyConfig(context.Background(), "dev-x", map[string]string{
					"environment.TOKEN": "s3cret",
				})
			},
			secret: "s3cret",
			want:   "environment.TOKEN=***",
		},
		{
			name: "device 追加時の値",
			call: func(c *incus.CLI) error {
				return c.ApplyDevices(context.Background(), "dev-x", map[string]incus.Device{
					"data": {"type": "disk", "source": "/s3cret/path"},
				})
			},
			stdout: `[{"name":"dev-x","devices":{}}]`,
			secret: "/s3cret/path",
			want:   "source=***",
		},
		{
			name: "device 更新時の値",
			call: func(c *incus.CLI) error {
				return c.ApplyDevices(context.Background(), "dev-x", map[string]incus.Device{
					"data": {"type": "disk", "source": "/s3cret/path"},
				})
			},
			stdout: `[{"name":"dev-x","devices":{"data":{"type":"disk","source":"/old"}}}]`,
			secret: "/s3cret/path",
			want:   "source=***",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &runnertest.Fake{}
			if tt.stdout != "" {
				f.Stdout = map[string]string{"incus list": tt.stdout}
			}
			c := &incus.CLI{Runner: f, Project: "default"}

			if err := tt.call(c); err != nil {
				t.Fatalf("error = %v", err)
			}

			display := f.LastCommand()
			if strings.Contains(display, tt.secret) {
				t.Errorf("表示 = %q, 値を含めないこと", display)
			}
			if !strings.Contains(display, tt.want) {
				t.Errorf("表示 = %q, %q を含むこと", display, tt.want)
			}

			// 実行される引数そのものは変えない
			raw := strings.Join(f.Calls[len(f.Calls)-1].Args, " ")
			if !strings.Contains(raw, tt.secret) {
				t.Errorf("実引数 = %q, 実際の値を渡すこと", raw)
			}
		})
	}
}
