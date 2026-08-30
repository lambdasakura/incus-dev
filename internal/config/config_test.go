package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
)

func parse(t *testing.T, yaml string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte(yaml), config.Options{})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	return c
}

func parseErr(t *testing.T, yaml string) error {
	t.Helper()
	c, err := config.Parse([]byte(yaml), config.Options{})
	if err == nil {
		t.Fatalf("Parse() = %+v, want error", c)
	}
	return err
}

const minimal = `
schema: 1
project:
  name: example-project
instance:
  image: images:ubuntu/24.04
`

func TestParseMinimal(t *testing.T) {
	c := parse(t, minimal)

	if c.Schema != 1 {
		t.Errorf("Schema = %d, want 1", c.Schema)
	}
	if c.Project.Name != "example-project" {
		t.Errorf("Project.Name = %q", c.Project.Name)
	}
	if c.Instance.Image != "images:ubuntu/24.04" {
		t.Errorf("Instance.Image = %q", c.Instance.Image)
	}
	if c.Runtime != nil {
		t.Errorf("Runtime = %+v, want nil", c.Runtime)
	}
	if len(c.Provision) != 0 {
		t.Errorf("Provision = %+v, want empty", c.Provision)
	}
}

// 省略時の既定値（仕様 3.7 / 3.6.3）
func TestDefaults(t *testing.T) {
	c := parse(t, minimal)

	ws := c.WorkspaceOrDefault()
	if ws.Source != "." || ws.Target != "/workspace" || ws.IDMap != config.IDMapAuto {
		t.Errorf("WorkspaceOrDefault() = %+v, want {., /workspace, auto}", ws)
	}
	if got := c.ProfileNames(); !cmp.Equal(got, []string{"default"}) {
		t.Errorf("ProfileNames() = %v, want [default]", got)
	}
	if c.Instance.TypeOrDefault() != "container" {
		t.Errorf("TypeOrDefault() = %q, want container", c.Instance.TypeOrDefault())
	}
}

// profiles: [] は「Profileを適用しない」を意味し、省略とは区別する（仕様 3.6.3）
func TestExplicitEmptyProfiles(t *testing.T) {
	c := parse(t, minimal+`
  profiles: []
  devices:
    root:
      type: disk
      pool: default
      path: /
`)
	if got := c.ProfileNames(); len(got) != 0 {
		t.Errorf("ProfileNames() = %v, want []", got)
	}
	if c.Instance.Profiles == nil {
		t.Error("Instance.Profiles = nil, 明示的な空リストと省略は区別すること")
	}
}

// bootstrap: [] は「bootstrapを行わない」を意味する（仕様 3.8）
func TestExplicitEmptyBootstrap(t *testing.T) {
	c := parse(t, minimal+`
bootstrap: []
`)
	if c.Bootstrap == nil {
		t.Fatal("Bootstrap = nil, 明示的な空リストと省略は区別すること")
	}
	if len(*c.Bootstrap) != 0 {
		t.Errorf("Bootstrap = %+v, want empty", *c.Bootstrap)
	}
}

// Incus config値のスカラは文字列へ正規化する（仕様 3.6.4）
func TestScalarNormalization(t *testing.T) {
	c := parse(t, minimal+`
  config:
    limits.cpu: 8
    limits.memory: 16GiB
    security.nesting: true
    some.float: 1.5
`)
	want := map[string]string{
		"limits.cpu":       "8",
		"limits.memory":    "16GiB",
		"security.nesting": "true",
		"some.float":       "1.5",
	}
	if diff := cmp.Diff(want, map[string]string(c.Instance.Config)); diff != "" {
		t.Errorf("Instance.Config mismatch (-want +got):\n%s", diff)
	}
}

func TestDevices(t *testing.T) {
	c := parse(t, minimal+`
  devices:
    gpu0:
      type: gpu
    http:
      type: proxy
      listen: tcp:127.0.0.1:8080
`)
	if got := c.Instance.Devices["gpu0"]["type"]; got != "gpu" {
		t.Errorf("devices.gpu0.type = %q", got)
	}
	if got := c.Instance.Devices["http"]["listen"]; got != "tcp:127.0.0.1:8080" {
		t.Errorf("devices.http.listen = %q", got)
	}
}

// run の短縮形（仕様 3.9.1）
func TestRunStepShortForm(t *testing.T) {
	c := parse(t, minimal+`
provision:
  - run: apt-get update
`)
	if len(c.Provision) != 1 {
		t.Fatalf("Provision len = %d, want 1", len(c.Provision))
	}
	step := c.Provision[0]
	if step.Run == nil {
		t.Fatal("Run = nil")
	}
	if step.Run.Script != "apt-get update" {
		t.Errorf("Run.Script = %q", step.Run.Script)
	}
	if step.Ansible != nil {
		t.Error("Ansible != nil")
	}
}

func TestRunStepFullForm(t *testing.T) {
	c := parse(t, minimal+`
provision:
  - name: install packages
    run: |
      apt-get update
      apt-get install -y jq
    shell: /bin/bash
    cwd: /workspace
    user: developer
    env:
      DEBIAN_FRONTEND: noninteractive
      RETRIES: 3
`)
	step := c.Provision[0]
	if step.Name != "install packages" {
		t.Errorf("Name = %q", step.Name)
	}
	if !strings.Contains(step.Run.Script, "apt-get install -y jq") {
		t.Errorf("Run.Script = %q", step.Run.Script)
	}
	if step.Run.Shell != "/bin/bash" || step.Run.Cwd != "/workspace" || step.Run.User != "developer" {
		t.Errorf("Run = %+v", step.Run)
	}
	want := map[string]string{"DEBIAN_FRONTEND": "noninteractive", "RETRIES": "3"}
	if diff := cmp.Diff(want, step.Run.Env); diff != "" {
		t.Errorf("Run.Env mismatch (-want +got):\n%s", diff)
	}
}

func TestAnsibleStep(t *testing.T) {
	c := parse(t, minimal+`
provision:
  - name: provision
    ansible:
      playbook: .incus-dev/ansible/site.yml
      vars: .incus-dev/ansible/vars.yml
      inventory: .incus-dev/ansible/hosts.yml
      tags: [setup]
      skip_tags: [slow]
      extra_args: ["--diff"]
`)
	a := c.Provision[0].Ansible
	if a == nil {
		t.Fatal("Ansible = nil")
	}
	if a.Playbook != ".incus-dev/ansible/site.yml" {
		t.Errorf("Playbook = %q", a.Playbook)
	}
	if a.Vars != ".incus-dev/ansible/vars.yml" || a.Inventory != ".incus-dev/ansible/hosts.yml" {
		t.Errorf("Ansible = %+v", a)
	}
	if !cmp.Equal(a.Tags, []string{"setup"}) || !cmp.Equal(a.SkipTags, []string{"slow"}) {
		t.Errorf("tags = %v, skip_tags = %v", a.Tags, a.SkipTags)
	}
	if !cmp.Equal(a.ExtraArgs, []string{"--diff"}) {
		t.Errorf("ExtraArgs = %v", a.ExtraArgs)
	}
}

func TestStepName(t *testing.T) {
	c := parse(t, minimal+`
provision:
  - run: echo a
  - name: named
    run: echo b
`)
	if got := c.Provision[0].DisplayName(1); got != "step 1" {
		t.Errorf("DisplayName() = %q, want %q", got, "step 1")
	}
	if got := c.Provision[1].DisplayName(2); got != "named" {
		t.Errorf("DisplayName() = %q, want %q", got, "named")
	}
}

func TestHasAnsibleStep(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{"provisionなし", minimal, false},
		{"runのみ", minimal + "provision:\n  - run: /bin/true\n", false},
		{"ansibleあり", minimal + "provision:\n  - ansible:\n      playbook: p.yml\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := config.Parse([]byte(tt.yaml), config.Options{})
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if got := c.HasAnsibleStep(); got != tt.want {
				t.Errorf("HasAnsibleStep() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string // エラーメッセージに含まれるべき文字列
	}{
		{
			name: "YAML構文エラー",
			yaml: "schema: 1\n  bad indent: [",
			want: "yaml",
		},
		{
			name: "schema欠落",
			yaml: "project:\n  name: p\ninstance:\n  image: i\n",
			want: "schema",
		},
		{
			name: "未知のschema version",
			yaml: "schema: 99\nproject:\n  name: p\ninstance:\n  image: i\n",
			want: "schema",
		},
		{
			name: "project.name欠落",
			yaml: "schema: 1\nproject: {}\ninstance:\n  image: i\n",
			want: "name",
		},
		{
			name: "instance.image欠落",
			yaml: "schema: 1\nproject:\n  name: p\ninstance: {}\n",
			want: "image",
		},
		{
			name: "トップレベルの未知フィールド",
			yaml: minimal + "packages:\n  - jq\n",
			want: "packages",
		},
		{
			name: "instanceの未知フィールド",
			yaml: minimal + "  resources:\n    cpu: 8\n",
			want: "resources",
		},
		{
			name: "featuresは廃止済み",
			yaml: minimal + "features:\n  python:\n    version: \"3.13\"\n",
			want: "features",
		},
		{
			name: "runとansibleの同時指定",
			yaml: minimal + "provision:\n  - run: echo\n    ansible:\n      playbook: p.yml\n",
			want: "provision[0]",
		},
		{
			name: "runもansibleも無い",
			yaml: minimal + "provision:\n  - name: empty\n",
			want: "provision[0]",
		},
		{
			name: "bootstrapにansibleステップ",
			yaml: minimal + "bootstrap:\n  - ansible:\n      playbook: p.yml\n",
			want: "bootstrap[0]",
		},
		{
			name: "ansibleステップにrun用フィールド",
			yaml: minimal + "provision:\n  - ansible:\n      playbook: p.yml\n    cwd: /workspace\n",
			want: "cwd",
		},
		{
			name: "予約済みconfigキー",
			yaml: minimal + "  config:\n    user.incus-devkit.project: x\n",
			want: "user.incus-devkit",
		},
		{
			name: "不正なproject名",
			yaml: "schema: 1\nproject:\n  name: \"bad name!\"\ninstance:\n  image: i\n",
			want: "name",
		},
		{
			name: "不正なidmap",
			yaml: minimal + "workspace:\n  idmap: magic\n",
			want: "idmap",
		},
		{
			name: "不正なinstance.type",
			yaml: minimal + "  type: pod\n",
			want: "type",
		},
		{
			name: "config値がオブジェクト",
			yaml: minimal + "  config:\n    limits.cpu:\n      nested: 1\n",
			want: "config",
		},
		{
			name: "playbook欠落のansibleステップ",
			yaml: minimal + "provision:\n  - ansible:\n      vars: v.yml\n",
			want: "playbook",
		},
		{
			name: "非互換なruntime version",
			yaml: "schema: 1\nruntime:\n  version: \"99.0\"\nproject:\n  name: p\ninstance:\n  image: i\n",
			want: "runtime",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseErr(t, tt.yaml)
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, %q を含むこと", err.Error(), tt.want)
			}
		})
	}
}

// 複数の問題をまとめて報告する（idev validate のUX）
func TestValidationReportsMultipleProblems(t *testing.T) {
	err := parseErr(t, "schema: 1\nproject: {}\ninstance: {}\n")
	msg := err.Error()
	for _, want := range []string{"name", "image"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, %q を含むこと", msg, want)
		}
	}
}

func TestRuntimeVersionCompatibility(t *testing.T) {
	tests := []struct {
		version string
		ok      bool
	}{
		{"1.0", true},
		{"1", true},
		{"1.0.0", true},
		{"1.99", false}, // 現行より新しいminorは満たせない
		{"2.0", false},
		{"0.9", false},
		{"abc", false},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			yaml := "schema: 1\nruntime:\n  version: \"" + tt.version + "\"\nproject:\n  name: p\ninstance:\n  image: i\n"
			_, err := config.Parse([]byte(yaml), config.Options{})
			if tt.ok && err != nil {
				t.Errorf("Parse() error = %v, want nil", err)
			}
			if !tt.ok && err == nil {
				t.Errorf("Parse() = nil error, want error")
			}
		})
	}
}

// 参照パスの存在検査（仕様 4.7）
func TestLoadChecksReferencedPaths(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".incus-dev", "dev.yml"), minimal+`
provision:
  - ansible:
      playbook: .incus-dev/ansible/site.yml
`)

	_, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err == nil {
		t.Fatal("Load() = nil error, playbookが存在しないのでエラーになること")
	}
	if !strings.Contains(err.Error(), "site.yml") {
		t.Errorf("error = %q, site.yml を含むこと", err.Error())
	}

	mustWrite(t, filepath.Join(root, ".incus-dev", "ansible", "site.yml"), "---\n")
	if _, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml")); err != nil {
		t.Errorf("Load() error = %v, playbookを配置したので成功すること", err)
	}
}

func TestLoadSetsRoot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".incus-dev", "dev.yml")
	mustWrite(t, path, minimal)

	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.Root != root {
		t.Errorf("Root = %q, want %q", c.Root, root)
	}
}

func TestLoadChecksWorkspaceSource(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".incus-dev", "dev.yml"), minimal+`
workspace:
  source: ./no-such-dir
`)
	_, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err == nil || !strings.Contains(err.Error(), "no-such-dir") {
		t.Errorf("Load() error = %v, workspace.source の不在を報告すること", err)
	}
}

func TestLoadChecksRelativeDeviceSource(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".incus-dev", "dev.yml"), minimal+`
  devices:
    data:
      type: disk
      source: ./assets
      path: /data
`)
	_, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err == nil || !strings.Contains(err.Error(), "assets") {
		t.Errorf("Load() error = %v, device sourceの不在を報告すること", err)
	}

	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml")); err != nil {
		t.Errorf("Load() error = %v", err)
	}
}

// 絶対パスのdevice sourceはホスト側の資源なので存在検査しない
func TestLoadSkipsAbsoluteDeviceSource(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".incus-dev", "dev.yml"), minimal+`
  devices:
    data:
      type: disk
      source: /srv/does-not-exist-on-purpose
      path: /data
`)
	if _, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml")); err != nil {
		t.Errorf("Load() error = %v, 絶対パスは検査しないこと", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// profiles: [] の場合、root diskはプロジェクト側で宣言する必要がある
func TestValidateRequiresRootDeviceWhenNoProfiles(t *testing.T) {
	err := parseErr(t, minimal+`
  profiles: []
`)
	for _, want := range []string{"root", "profiles"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, %q を含むこと", err.Error(), want)
		}
	}
}

func TestValidateAcceptsExplicitRootDevice(t *testing.T) {
	parse(t, minimal+`
  profiles: []
  devices:
    root:
      type: disk
      pool: default
      path: /
`)
}

// Profileを使う場合はroot diskの宣言を要求しない
func TestValidateDoesNotRequireRootDeviceWithProfiles(t *testing.T) {
	parse(t, minimal)
}

// idmapの各モード（仕様 03-configuration.md 3.7.3）
func TestWorkspaceIDMapModes(t *testing.T) {
	tests := []struct {
		value string
		want  config.IDMapMode
		ok    bool
	}{
		{"auto", config.IDMapAuto, true},
		{"raw", config.IDMapRaw, true},
		{"shift", config.IDMapShift, true},
		{"none", config.IDMapNone, true},
		{"magic", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			c, err := config.Parse([]byte(minimal+"workspace:\n  idmap: "+tt.value+"\n"), config.Options{})
			if !tt.ok {
				if err == nil {
					t.Fatal("Parse() = nil error, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if got := c.WorkspaceOrDefault().IDMap; got != tt.want {
				t.Errorf("IDMap = %q, want %q", got, tt.want)
			}
		})
	}
}

// incusコマンドのフラグとして解釈されうるキーを拒否する
func TestValidateRejectsFlagLikeKeys(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{"config キー", minimal + "  config:\n    \"--project\": other\n"},
		{"device 名", minimal + "  devices:\n    \"--help\":\n      type: none\n"},
		{"device キー", minimal + "  devices:\n    data:\n      type: disk\n      \"--force\": \"1\"\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseErr(t, tt.yaml)
			if !strings.Contains(err.Error(), "must not start with") {
				t.Errorf("error = %q", err.Error())
			}
		})
	}
}

// pool を伴う disk の source はストレージボリューム名なので、パスとして検査しない
func TestLoadAllowsStorageVolumeSource(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".incus-dev", "dev.yml"), minimal+`
  devices:
    data:
      type: disk
      pool: default
      source: myvolume
      path: /data
`)
	if _, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml")); err != nil {
		t.Errorf("Load() error = %v, ボリューム名をパスとして扱わないこと", err)
	}
}

// 予約されたdevice名は使えない
func TestValidateRejectsReservedDeviceName(t *testing.T) {
	err := parseErr(t, minimal+`
  devices:
    workspace:
      type: disk
      source: /tmp
      path: /elsewhere
`)
	if !strings.Contains(err.Error(), "workspace") {
		t.Errorf("error = %q", err.Error())
	}
}

// 参照先の問題は「存在しない」以外の理由も報告する
func TestLoadReportsStatError(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".incus-dev", "dev.yml"), minimal+`
workspace:
  source: ./missing
`)
	_, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("error = %v", err)
	}
}

func TestLoadReadError(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "no-such-file.yml"))
	if err == nil || !strings.Contains(err.Error(), "read") {
		t.Errorf("error = %v", err)
	}
}

func TestParseRejectsNonMappingDocument(t *testing.T) {
	for _, in := range []string{"- a\n- b\n", "just a string\n", "42\n"} {
		if _, err := config.Parse([]byte(in), config.Options{}); err == nil {
			t.Errorf("Parse(%q) = nil error, want error", in)
		}
	}
}

func TestParseRejectsNonNumericSchema(t *testing.T) {
	err := parseErr(t, "schema: \"1\"\nproject:\n  name: p\ninstance:\n  image: i\n")
	if !strings.Contains(err.Error(), "integer") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestInstanceTypeExplicit(t *testing.T) {
	c := parse(t, minimal+"  type: virtual-machine\n")

	if got := c.Instance.TypeOrDefault(); got != "virtual-machine" {
		t.Errorf("TypeOrDefault() = %q", got)
	}
}

func TestResolvePath(t *testing.T) {
	c := parse(t, minimal)
	c.Root = "/root"

	tests := map[string]string{
		"":           "/root",
		"rel":        "/root/rel",
		"/absolute":  "/absolute",
		"./nested/x": "/root/nested/x",
	}
	for in, want := range tests {
		if got := c.ResolvePath(in); got != want {
			t.Errorf("ResolvePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWorkspaceOrDefaultPartialOverride(t *testing.T) {
	c := parse(t, minimal+"workspace:\n  target: /src\n")

	ws := c.WorkspaceOrDefault()
	if ws.Target != "/src" || ws.Source != "." || ws.IDMap != config.IDMapAuto {
		t.Errorf("WorkspaceOrDefault() = %+v", ws)
	}
}

// bootstrap 内のパスも検査する
func TestLoadChecksBootstrapPaths(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".incus-dev", "dev.yml"), minimal+`
bootstrap:
  - run: echo hi
provision:
  - ansible:
      playbook: .incus-dev/ansible/site.yml
`)
	_, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err == nil || !strings.Contains(err.Error(), "site.yml") {
		t.Errorf("error = %v", err)
	}
}

func TestWorkspaceTargetMustBeAbsolute(t *testing.T) {
	err := parseErr(t, minimal+"workspace:\n  target: relative/path\n")
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestWorkspaceSourceMustBeDirectory(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "afile"), "x")
	mustWrite(t, filepath.Join(root, ".incus-dev", "dev.yml"), minimal+`
workspace:
  source: ./afile
`)
	_, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error = %v", err)
	}
}

// Profile名の構文を検査する（仕様 04-cli.md 4.7）
func TestValidateProfileNameSyntax(t *testing.T) {
	invalid := []string{"bad name", "../escape", "-leading", "with/slash", ""}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			err := parseErr(t, minimal+"  profiles:\n    - \""+name+"\"\n")
			if !strings.Contains(err.Error(), "profile") && !strings.Contains(err.Error(), "profiles") {
				t.Errorf("error = %q", err.Error())
			}
		})
	}

	parse(t, minimal+"  profiles:\n    - default\n    - gpu-nvidia\n    - my.profile_1\n")
}
