package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/lambdasakura/incus-dev/internal/config"
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

// The defaults when things are omitted (spec 3.7 and 3.6.3).
func TestDefaults(t *testing.T) {
	c := parse(t, minimal)

	ws := c.WorkspaceOrDefault()
	if ws.Source != "." || ws.Target != "/workspace" || ws.IDMap != config.IDMapAuto {
		t.Errorf("WorkspaceOrDefault() = %+v, want {., /workspace, auto}", ws)
	}
	if got := c.ProfileNames(); !cmp.Equal(got, []string{"default"}) {
		t.Errorf("ProfileNames() = %v, want [default]", got)
	}
}

// profiles: [] means "apply no profile", which is not the same as omitting it
// (spec 3.6.3).
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
		t.Error("Instance.Profiles = nil, want an explicit empty list kept distinct from an omission")
	}
}

// bootstrap: [] means "do not bootstrap" (spec 3.8).
func TestExplicitEmptyBootstrap(t *testing.T) {
	c := parse(t, minimal+`
bootstrap: []
`)
	if c.Bootstrap == nil {
		t.Fatal("Bootstrap = nil, want an explicit empty list kept distinct from an omission")
	}
	if len(*c.Bootstrap) != 0 {
		t.Errorf("Bootstrap = %+v, want empty", *c.Bootstrap)
	}
}

// Scalar values in the Incus config are normalised to strings (spec 3.6.4).
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

// The short form of run (spec 3.9.1).
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
		{"no provision", minimal, false},
		{"run only", minimal + "provision:\n  - run: /bin/true\n", false},
		{"with ansible", minimal + "provision:\n  - ansible:\n      playbook: p.yml\n", true},
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
		want string // a string the error message must contain
	}{
		{
			name: "YAML syntax error",
			yaml: "schema: 1\n  bad indent: [",
			want: "yaml",
		},
		{
			name: "schema missing",
			yaml: "project:\n  name: p\ninstance:\n  image: i\n",
			want: "schema",
		},
		{
			name: "unknown schema version",
			yaml: "schema: 99\nproject:\n  name: p\ninstance:\n  image: i\n",
			want: "schema",
		},
		{
			name: "project.name missing",
			yaml: "schema: 1\nproject: {}\ninstance:\n  image: i\n",
			want: "name",
		},
		{
			name: "instance.image missing",
			yaml: "schema: 1\nproject:\n  name: p\ninstance: {}\n",
			want: "image",
		},
		{
			name: "unknown top-level field",
			yaml: minimal + "packages:\n  - jq\n",
			want: "packages",
		},
		{
			name: "unknown field under instance",
			yaml: minimal + "  resources:\n    cpu: 8\n",
			want: "resources",
		},
		{
			name: "features has been removed",
			yaml: minimal + "features:\n  python:\n    version: \"3.13\"\n",
			want: "features",
		},
		{
			name: "run and ansible together",
			yaml: minimal + "provision:\n  - run: echo\n    ansible:\n      playbook: p.yml\n",
			want: "provision[0]",
		},
		{
			name: "neither run nor ansible",
			yaml: minimal + "provision:\n  - name: empty\n",
			want: "provision[0]",
		},
		{
			name: "an ansible step in bootstrap",
			yaml: minimal + "bootstrap:\n  - ansible:\n      playbook: p.yml\n",
			want: "bootstrap[0]",
		},
		{
			name: "a run-only field on an ansible step",
			yaml: minimal + "provision:\n  - ansible:\n      playbook: p.yml\n    cwd: /workspace\n",
			want: "cwd",
		},
		{
			name: "reserved config key",
			yaml: minimal + "  config:\n    user.incus-devkit.project: x\n",
			want: "user.incus-dev",
		},
		{
			name: "invalid project name",
			yaml: "schema: 1\nproject:\n  name: \"bad name!\"\ninstance:\n  image: i\n",
			want: "name",
		},
		{
			name: "invalid idmap",
			yaml: minimal + "workspace:\n  idmap: magic\n",
			want: "idmap",
		},
		{
			name: "an object as a config value",
			yaml: minimal + "  config:\n    limits.cpu:\n      nested: 1\n",
			want: "config",
		},
		{
			name: "an ansible step without a playbook",
			yaml: minimal + "provision:\n  - ansible:\n      vars: v.yml\n",
			want: "playbook",
		},
		{
			name: "incompatible runtime version",
			yaml: "schema: 1\nruntime:\n  version: \"99.0\"\nproject:\n  name: p\ninstance:\n  image: i\n",
			want: "runtime",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseErr(t, tt.yaml)
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.want)
			}
		})
	}
}

// Every problem is reported at once, which is what makes idev validate useful.
func TestValidationReportsMultipleProblems(t *testing.T) {
	err := parseErr(t, "schema: 1\nproject: {}\ninstance: {}\n")
	msg := err.Error()
	for _, want := range []string{"name", "image"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to contain %q", msg, want)
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
		{"1.99", false}, // a minor newer than the current one cannot be satisfied
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

// Referenced paths are checked for existence (spec 4.7).
func TestLoadChecksReferencedPaths(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".incus-dev", "dev.yml"), minimal+`
provision:
  - ansible:
      playbook: .incus-dev/ansible/site.yml
`)

	_, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err == nil {
		t.Fatal("Load() = nil error, want an error because the playbook is missing")
	}
	if !strings.Contains(err.Error(), "site.yml") {
		t.Errorf("error = %q, want it to contain site.yml", err.Error())
	}

	mustWrite(t, filepath.Join(root, ".incus-dev", "ansible", "site.yml"), "---\n")
	if _, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml")); err != nil {
		t.Errorf("Load() error = %v, want success now that the playbook is in place", err)
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
		t.Errorf("Load() error = %v, want the missing workspace.source reported", err)
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
		t.Errorf("Load() error = %v, want the missing device source reported", err)
	}

	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml")); err != nil {
		t.Errorf("Load() error = %v", err)
	}
}

// An absolute device source is a host resource, so its existence is not checked.
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
		t.Errorf("Load() error = %v, want absolute paths left unchecked", err)
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

// With profiles: [], the project has to declare the root disk itself.
func TestValidateRequiresRootDeviceWhenNoProfiles(t *testing.T) {
	err := parseErr(t, minimal+`
  profiles: []
`)
	for _, want := range []string{"root", "profiles"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), want)
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

// With a profile in play, no root disk declaration is required.
func TestValidateDoesNotRequireRootDeviceWithProfiles(t *testing.T) {
	parse(t, minimal)
}

// Each idmap mode (spec 03-configuration.md 3.7.3).
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

// Keys that could be taken for flags of the incus command are rejected.
func TestValidateRejectsFlagLikeKeys(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{"config key", minimal + "  config:\n    \"--project\": other\n"},
		{"device name", minimal + "  devices:\n    \"--help\":\n      type: none\n"},
		{"device key", minimal + "  devices:\n    data:\n      type: disk\n      \"--force\": \"1\"\n"},
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

// The source of a disk with a pool is a volume name, so it is not checked as a
// path.
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
		t.Errorf("Load() error = %v, want a volume name not treated as a path", err)
	}
}

// The reserved device name cannot be used.
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

// A problem with a referenced path is reported even when it is not "missing".
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

// Only containers are supported, so instance.type is not a setting at all
// (spec 03-configuration.md 3.4).
func TestInstanceTypeIsRejected(t *testing.T) {
	for _, value := range []string{"virtual-machine", "container"} {
		t.Run(value, func(t *testing.T) {
			err := parseErr(t, minimal+"  type: "+value+"\n")
			if !strings.Contains(err.Error(), "type") {
				t.Errorf("error = %q, want instance.type to be rejected", err.Error())
			}
		})
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

// Paths inside bootstrap are checked too.
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

// Profile names are checked for syntax (spec 04-cli.md 4.7).
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

// Values that could be taken for options of incus or su are rejected.
func TestValidateRejectsFlagLikeValues(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{"image", "schema: 1\nproject:\n  name: p\ninstance:\n  image: \"--project=other\"\n"},
		{"run.user", minimal + "provision:\n  - run: echo\n    user: \"-lc\"\n"},
		{"run.shell", minimal + "provision:\n  - run: echo\n    shell: \"--login\"\n"},
		{"bootstrap.user", minimal + "bootstrap:\n  - run: echo\n    user: \"-x\"\n"},
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

// A root disk is "type: disk and path: /" (spec 03-configuration.md 3.6.3).
func TestValidateRootDiskRequiresRootPath(t *testing.T) {
	// A disk, but not one that provides /.
	err := parseErr(t, minimal+`
  profiles: []
  devices:
    data:
      type: disk
      pool: default
      path: /data
`)
	if !strings.Contains(err.Error(), "root") {
		t.Errorf("error = %q, want the missing root disk reported", err.Error())
	}

	// A non-disk device with path: / is not a root disk either.
	err = parseErr(t, minimal+`
  profiles: []
  devices:
    weird:
      type: none
      path: /
`)
	if !strings.Contains(err.Error(), "root") {
		t.Errorf("error = %q", err.Error())
	}
}

// incus splits k=v at the first =, so a key cannot contain one.
func TestValidateRejectsEqualsInConfigKey(t *testing.T) {
	err := parseErr(t, minimal+"  config:\n    \"limits.cpu=8\": \"x\"\n")
	if !strings.Contains(err.Error(), "=") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestValidateRejectsEqualsInDeviceKey(t *testing.T) {
	err := parseErr(t, minimal+"  devices:\n    data:\n      type: disk\n      \"a=b\": x\n")
	if !strings.Contains(err.Error(), "=") {
		t.Errorf("error = %q", err.Error())
	}
}

// The shell section (spec 3.13).
func TestShellSettings(t *testing.T) {
	c := parse(t, minimal+`
shell:
  user: developer
  command: /bin/bash
  cwd: /workspace/src
`)
	sh := c.ShellOrDefault()
	if sh.User != "developer" || sh.Command != "/bin/bash" || sh.Cwd != "/workspace/src" {
		t.Errorf("ShellOrDefault() = %+v", sh)
	}
}

func TestShellDefaults(t *testing.T) {
	c := parse(t, minimal)

	sh := c.ShellOrDefault()
	if sh.Command != config.DefaultShell {
		t.Errorf("Command = %q, want %q", sh.Command, config.DefaultShell)
	}
	if sh.Cwd != "/workspace" {
		t.Errorf("Cwd = %q, want workspace.target as the default", sh.Cwd)
	}
	if sh.User != "" {
		t.Errorf("User = %q, want no user by default", sh.User)
	}
}

// Changing workspace.target moves the shell default with it.
func TestShellCwdFollowsWorkspace(t *testing.T) {
	c := parse(t, minimal+"workspace:\n  target: /src\n")

	if got := c.ShellOrDefault().Cwd; got != "/src" {
		t.Errorf("Cwd = %q, want /src", got)
	}
}

// The incus section (spec 3.13).
func TestIncusProjectSetting(t *testing.T) {
	c := parse(t, minimal+"incus:\n  project: development\n")

	if c.Incus == nil || c.Incus.Project != "development" {
		t.Errorf("Incus = %+v", c.Incus)
	}
}

func TestValidateRejectsFlagLikeShellValues(t *testing.T) {
	for _, y := range []string{
		minimal + "shell:\n  user: \"-x\"\n",
		minimal + "shell:\n  command: \"--login\"\n",
	} {
		if err := parseErr(t, y); !strings.Contains(err.Error(), "must not start with") {
			t.Errorf("error = %q", err.Error())
		}
	}
}

func TestValidateShellCwdMustBeAbsolute(t *testing.T) {
	err := parseErr(t, minimal+"shell:\n  cwd: relative\n")
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error = %q", err.Error())
	}
}

// The galaxy step (spec 06-provisioning.md 6.5.5).
func TestGalaxyStep(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".incus-dev", "ansible", "requirements.yml"), "collections: []\n")
	mustWrite(t, filepath.Join(root, ".incus-dev", "dev.yml"), minimal+`
provision:
  - name: collections
    galaxy:
      requirements: .incus-dev/ansible/requirements.yml
      extra_args: ["--force"]
`)

	c, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	g := c.Provision[0].Galaxy
	if g == nil {
		t.Fatal("Galaxy = nil")
	}
	if g.Requirements != ".incus-dev/ansible/requirements.yml" {
		t.Errorf("Requirements = %q", g.Requirements)
	}
	if len(g.ExtraArgs) != 1 || g.ExtraArgs[0] != "--force" {
		t.Errorf("ExtraArgs = %v", g.ExtraArgs)
	}
}

func TestGalaxyStepErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "requirements missing",
			yaml: minimal + "provision:\n  - galaxy: {}\n",
			want: "requirements",
		},
		{
			name: "together with run",
			yaml: minimal + "provision:\n  - run: echo\n    galaxy:\n      requirements: r.yml\n",
			want: "provision[0]",
		},
		{
			name: "together with ansible",
			yaml: minimal + "provision:\n  - ansible:\n      playbook: p.yml\n    galaxy:\n      requirements: r.yml\n",
			want: "provision[0]",
		},
		{
			name: "not allowed in bootstrap",
			yaml: minimal + "bootstrap:\n  - galaxy:\n      requirements: r.yml\n",
			want: "bootstrap[0]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := parseErr(t, tt.yaml); !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.want)
			}
		})
	}
}

func TestGalaxyRequirementsMustExist(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".incus-dev", "dev.yml"), minimal+`
provision:
  - galaxy:
      requirements: .incus-dev/ansible/requirements.yml
`)
	_, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err == nil || !strings.Contains(err.Error(), "requirements.yml") {
		t.Errorf("error = %v", err)
	}
}

// volumes (spec 03-configuration.md 3.16).
func TestVolumes(t *testing.T) {
	c := parse(t, minimal+`
volumes:
  cache:
    path: /home/dev/.cache
    size: 10GiB
  data:
    path: /var/lib/postgresql
    pool: fast
`)
	if got := c.Volumes["cache"].Path; got != "/home/dev/.cache" {
		t.Errorf("cache.path = %q", got)
	}
	if got := c.Volumes["cache"].PoolOrDefault(); got != "default" {
		t.Errorf("cache pool = %q, want the default pool", got)
	}
	if got := c.Volumes["data"].PoolOrDefault(); got != "fast" {
		t.Errorf("data pool = %q", got)
	}
	if got := c.Volumes["cache"].Size; got != "10GiB" {
		t.Errorf("cache.size = %q", got)
	}
}

func TestVolumeErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"path missing", minimal + "volumes:\n  cache: {}\n", "path"},
		{"relative path", minimal + "volumes:\n  cache:\n    path: rel\n", "absolute"},
		{"reserved name", minimal + "volumes:\n  workspace:\n    path: /w\n", "reserved"},
		{
			name: "collides with a device",
			yaml: minimal + "  devices:\n    cache:\n      type: disk\n      source: /tmp\n      path: /c\n" +
				"volumes:\n  cache:\n    path: /cache\n",
			want: "conflicts",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := parseErr(t, tt.yaml); !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.want)
			}
		})
	}
}

// secrets (spec 03-configuration.md 3.12).
func TestSecrets(t *testing.T) {
	c := parse(t, minimal+`
secrets:
  API_TOKEN:
    env: MY_TOKEN
  DEPLOY_KEY:
    file: ~/.config/key
  OPTIONAL_ONE:
    env: MAYBE
    optional: true
`)
	if got := c.Secrets["API_TOKEN"].Env; got != "MY_TOKEN" {
		t.Errorf("API_TOKEN.env = %q", got)
	}
	if got := c.Secrets["DEPLOY_KEY"].File; got != "~/.config/key" {
		t.Errorf("DEPLOY_KEY.file = %q", got)
	}
	if !c.Secrets["OPTIONAL_ONE"].Optional {
		t.Error("optional did not take effect")
	}
	if got := c.Secrets["API_TOKEN"].Source(); !strings.Contains(got, "MY_TOKEN") {
		t.Errorf("Source() = %q", got)
	}
}

func TestSecretErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"env and file together", minimal + "secrets:\n  A:\n    env: X\n    file: /f\n", "mutually exclusive"},
		{"neither given", minimal + "secrets:\n  A:\n    optional: true\n", "must specify"},
		{"a name devkit reserves", minimal + "secrets:\n  DEVKIT_TOKEN:\n    env: X\n", "reserved"},
		{"not a valid environment variable name", minimal + "secrets:\n  \"bad-name\":\n    env: X\n", "bad-name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := parseErr(t, tt.yaml); !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.want)
			}
		})
	}
}
