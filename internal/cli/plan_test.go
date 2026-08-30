package cli

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
)

func mustParse(t *testing.T, yaml string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte(yaml), config.Options{})
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	c.Root = "/home/u/src/example"
	return c
}

const planBase = `
schema: 1
project:
  name: example-project
instance:
  image: images:ubuntu/24.04
`

func TestDesiredDevicesIncludesWorkspace(t *testing.T) {
	cfg := mustParse(t, planBase)

	devices := desiredDevices(cfg, config.IDMapRaw)

	want := map[string]string{
		"type":   "disk",
		"source": "/home/u/src/example",
		"path":   "/workspace",
		"shift":  "false",
	}
	if diff := cmp.Diff(want, map[string]string(devices["workspace"])); diff != "" {
		t.Errorf("workspace device mismatch (-want +got):\n%s", diff)
	}
}

func TestDesiredDevicesResolvesRelativeSource(t *testing.T) {
	cfg := mustParse(t, planBase+`
  devices:
    data:
      type: disk
      source: ./assets
      path: /data
`)
	devices := desiredDevices(cfg, config.IDMapRaw)

	if got := devices["data"]["source"]; got != "/home/u/src/example/assets" {
		t.Errorf("source = %q, project rootを基準に解決すること", got)
	}
}

func TestDesiredDevicesKeepsAbsoluteSource(t *testing.T) {
	cfg := mustParse(t, planBase+`
  devices:
    data:
      type: disk
      source: /srv/data
      path: /data
`)
	if got := desiredDevices(cfg, config.IDMapRaw)["data"]["source"]; got != "/srv/data" {
		t.Errorf("source = %q", got)
	}
}

func TestDesiredConfigIncludesManagedMarkers(t *testing.T) {
	cfg := mustParse(t, planBase+`
  config:
    limits.cpu: "8"
`)
	got := desiredConfig(cfg, config.IDMapRaw, 1000, 1000)

	if got["limits.cpu"] != "8" {
		t.Errorf("limits.cpu = %q", got["limits.cpu"])
	}
	if got["user.incus-devkit.project"] != "example-project" {
		t.Errorf("user.incus-devkit.project = %q", got["user.incus-devkit.project"])
	}
	if got["user.incus-devkit.root"] != "/home/u/src/example" {
		t.Errorf("user.incus-devkit.root = %q", got["user.incus-devkit.root"])
	}
}

// プロジェクトが raw.idmap を明示した場合はそちらを尊重する
func TestDesiredConfigDoesNotOverrideExplicitIDMap(t *testing.T) {
	cfg := mustParse(t, planBase+`
  config:
    raw.idmap: "both 1234 0"
`)
	if got := desiredConfig(cfg, config.IDMapRaw, 1000, 1000)["raw.idmap"]; got != "both 1234 0" {
		t.Errorf("raw.idmap = %q, 明示指定を上書きしないこと", got)
	}
}

func TestIsManagedBy(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]string
		want   bool
	}{
		{"管理下", map[string]string{"user.incus-devkit.project": "example-project"}, true},
		{"印なし", map[string]string{}, false},
		{"別プロジェクト", map[string]string{"user.incus-devkit.project": "other"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isManagedBy(tt.config, "example-project"); got != tt.want {
				t.Errorf("isManagedBy() = %v, want %v", got, tt.want)
			}
		})
	}
}
