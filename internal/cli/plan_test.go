package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/incus"
	"github.com/lambdasakura/incus-dev/internal/runner/runnertest"
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

	devices := desiredDevices(cfg, rawPlan, "dev-example-project")

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
	devices := desiredDevices(cfg, rawPlan, "dev-example-project")

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
	if got := desiredDevices(cfg, rawPlan, "dev-example-project")["data"]["source"]; got != "/srv/data" {
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

	got := staleDevices(current, desiredDevices(cfg, rawPlan, "dev-example-project"))
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

// 同一マシンで複数checkoutを扱えるようにする（仕様 05-incus.md 5.1）
func TestInstanceNameScope(t *testing.T) {
	tests := []struct {
		name   string
		scope  string
		root   string
		branch string
		want   string
	}{
		{"既定は名前のみ", "", "/home/u/a", "main", "dev-example-project"},
		{"name", "name", "/home/u/a", "main", "dev-example-project"},
		{"branch", "branch", "/home/u/a", "feature/x", "dev-example-project-feature-x"},
		{"branchが既定名", "branch", "/home/u/a", "main", "dev-example-project-main"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := mustParse(t, planBase)
			if tt.scope != "" {
				cfg.Project.Scope = config.Scope(tt.scope)
			}
			cfg.Root = tt.root

			got, err := instanceNameFor(cfg, func() (string, error) { return tt.branch, nil })
			if err != nil {
				t.Fatalf("instanceNameFor() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("instanceNameFor() = %q, want %q", got, tt.want)
			}
		})
	}
}

// path スコープは checkout ごとに異なる名前になる
func TestInstanceNameScopePath(t *testing.T) {
	name := func(root string) string {
		cfg := mustParse(t, planBase)
		cfg.Project.Scope = config.ScopePath
		cfg.Root = root

		got, err := instanceNameFor(cfg, nil)
		if err != nil {
			t.Fatalf("instanceNameFor() error = %v", err)
		}
		return got
	}

	a, b := name("/home/u/checkout-a"), name("/home/u/checkout-b")
	if a == b {
		t.Errorf("checkoutが違っても同じ名前になっている: %q", a)
	}
	if a != name("/home/u/checkout-a") {
		t.Error("同じパスでは同じ名前になること")
	}
	for _, got := range []string{a, b} {
		if !strings.HasPrefix(got, "dev-example-project-") {
			t.Errorf("name = %q", got)
		}
	}
}

// branch を取得できない場合は、対処が分かるエラーにする
func TestInstanceNameScopeBranchError(t *testing.T) {
	cfg := mustParse(t, planBase)
	cfg.Project.Scope = config.ScopeBranch

	_, err := instanceNameFor(cfg, func() (string, error) { return "", errors.New("not a git repository") })
	if err == nil || !strings.Contains(err.Error(), "branch") {
		t.Errorf("error = %v", err)
	}
}

// ブランチ名の取得は、コミットが無いリポジトリやdetached HEADでも成り立つこと
func TestGitBranch(t *testing.T) {
	tests := []struct {
		name    string
		fake    *runnertest.Fake
		want    string
		wantErr bool
	}{
		{
			name: "通常のブランチ",
			fake: &runnertest.Fake{Stdout: map[string]string{"git -C /r symbolic-ref": "feature/x\n"}},
			want: "feature/x",
		},
		{
			name: "detached HEAD ではコミットハッシュ",
			fake: &runnertest.Fake{
				Err:    map[string]error{"git -C /r symbolic-ref": errors.New("not a symbolic ref")},
				Stdout: map[string]string{"git -C /r rev-parse": "a8f213\n"},
			},
			want: "a8f213",
		},
		{
			name:    "Gitリポジトリでない",
			fake:    &runnertest.Fake{Err: map[string]error{"git": errors.New("not a git repository")}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gitBranch(context.Background(), tt.fake, "/r")()

			if tt.wantErr {
				if err == nil {
					t.Fatalf("gitBranch() = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("gitBranch() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("gitBranch() = %q, want %q", got, tt.want)
			}
		})
	}
}
