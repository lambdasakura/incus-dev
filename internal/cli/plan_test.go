package cli

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus"
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

// rawPlan は raw 方式で解決済みの計画。
var rawPlan = idmapPlan{Mode: config.IDMapRaw, Managed: true, UID: 1000, GID: 1000}

const planBase = `
schema: 1
project:
  name: example-project
instance:
  image: images:ubuntu/24.04
`

func TestDesiredDevicesIncludesWorkspace(t *testing.T) {
	cfg := mustParse(t, planBase)

	devices := desiredDevices(cfg, rawPlan)

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
	devices := desiredDevices(cfg, rawPlan)

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
	if got := desiredDevices(cfg, rawPlan)["data"]["source"]; got != "/srv/data" {
		t.Errorf("source = %q", got)
	}
}

func TestDesiredConfigIncludesManagedMarkers(t *testing.T) {
	cfg := mustParse(t, planBase+`
  config:
    limits.cpu: "8"
`)
	got := desiredConfig(cfg, rawPlan)

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
	plan, err := resolveIDMap(cfg, 1000, 1000, permitted)
	if err != nil {
		t.Fatal(err)
	}

	if got := desiredConfig(cfg, plan)["raw.idmap"]; got != "both 1234 0" {
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

// devkitが適用したキーを記録し、削除に追従できるようにする（仕様 05-incus.md 5.4.4）
func TestDesiredConfigRecordsManagedKeys(t *testing.T) {
	cfg := mustParse(t, planBase+`
  config:
    limits.cpu: "8"
    limits.memory: 16GiB
`)
	got := desiredConfig(cfg, rawPlan)[managedKeysKey]

	if got != "limits.cpu,limits.memory,raw.idmap" {
		t.Errorf("%s = %q", managedKeysKey, got)
	}
}

func TestStaleConfigKeys(t *testing.T) {
	tests := []struct {
		name    string
		current map[string]string
		yaml    string
		want    []string
	}{
		{
			name: "宣言から消えたキーを取り消す",
			current: map[string]string{
				managedKeysKey:  "limits.cpu,limits.memory,raw.idmap",
				"limits.cpu":    "8",
				"limits.memory": "16GiB",
			},
			yaml: planBase + "  config:\n    limits.cpu: \"8\"\n",
			want: []string{"limits.memory"},
		},
		{
			name: "利用者が手で足したキーには触れない",
			current: map[string]string{
				managedKeysKey:     "limits.cpu,raw.idmap",
				"security.nesting": "true", // devkit管理外
			},
			yaml: planBase + "  config:\n    limits.cpu: \"8\"\n",
			want: nil,
		},
		{
			name:    "記録が無い場合はidmapのみ追従する",
			current: map[string]string{idmapConfigKey: "uid 1000 0"},
			yaml:    planBase + "workspace:\n  idmap: shift\n",
			want:    []string{idmapConfigKey},
		},
		{
			name:    "記録も差分も無ければ何もしない",
			current: map[string]string{managedKeysKey: "raw.idmap", idmapConfigKey: "uid 1000 0"},
			yaml:    planBase,
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := mustParse(t, tt.yaml)
			plan, err := resolveIDMap(cfg, 1000, 1000, permitted)
			if err != nil {
				t.Fatal(err)
			}

			got := staleConfigKeys(tt.current, desiredConfig(cfg, plan), plan)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("staleConfigKeys() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// deviceも同様に、devkitが作ったものだけ削除に追従する
func TestStaleDevices(t *testing.T) {
	cfg := mustParse(t, planBase+`
  devices:
    keep:
      type: disk
      source: /srv/a
      path: /a
`)
	current := &incus.Instance{
		Config: map[string]string{managedDevicesKey: "gone,keep,workspace"},
		Devices: map[string]incus.Device{
			"keep":      {"type": "disk"},
			"gone":      {"type": "disk"}, // 宣言から消えた
			"manual":    {"type": "nic"},  // 利用者が手で追加
			"workspace": {"type": "disk"},
		},
	}

	got := staleDevices(current, desiredDevices(cfg, rawPlan))
	if diff := cmp.Diff([]string{"gone"}, got); diff != "" {
		t.Errorf("staleDevices() mismatch (-want +got):\n%s", diff)
	}
}

func TestDesiredConfigRecordsManagedDevices(t *testing.T) {
	cfg := mustParse(t, planBase+`
  devices:
    data:
      type: disk
      source: /srv/a
      path: /a
`)
	got := desiredConfig(cfg, rawPlan)[managedDevicesKey]

	if got != "data,workspace" {
		t.Errorf("%s = %q", managedDevicesKey, got)
	}
}
