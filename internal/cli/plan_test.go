package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

// rawPlan is a plan already resolved to the raw strategy.
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
		t.Errorf("source = %q, want it resolved from the project root", got)
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
	got := desiredConfig(cfg, rawPlan, nil, "dev-example-project")

	if got["limits.cpu"] != "8" {
		t.Errorf("limits.cpu = %q", got["limits.cpu"])
	}
	if got["user.incus-dev.project"] != "example-project" {
		t.Errorf("user.incus-dev.project = %q", got["user.incus-dev.project"])
	}
	if got["user.incus-dev.root"] != "/home/u/src/example" {
		t.Errorf("user.incus-dev.root = %q", got["user.incus-dev.root"])
	}
}

// A raw.idmap the project set explicitly wins.
func TestDesiredConfigDoesNotOverrideExplicitIDMap(t *testing.T) {
	cfg := mustParse(t, planBase+`
  config:
    raw.idmap: "both 1234 0"
`)
	plan, err := resolveIDMap(cfg, 1000, 1000, permitted)
	if err != nil {
		t.Fatal(err)
	}

	if got := desiredConfig(cfg, plan, nil, "dev-example-project")["raw.idmap"]; got != "both 1234 0" {
		t.Errorf("raw.idmap = %q, want the explicit setting left alone", got)
	}
}

func TestIsManagedBy(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]string
		want   bool
	}{
		{"managed", map[string]string{"user.incus-dev.project": "example-project"}, true},
		{"unmarked", map[string]string{}, false},
		{"a different project", map[string]string{"user.incus-dev.project": "other"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isManagedBy(tt.config, "example-project"); got != tt.want {
				t.Errorf("isManagedBy() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The keys idev applied are recorded, so a removal can be followed
// (spec 05-incus.md 5.4.4).
func TestDesiredConfigRecordsManagedKeys(t *testing.T) {
	cfg := mustParse(t, planBase+`
  config:
    limits.cpu: "8"
    limits.memory: 16GiB
`)
	got := desiredConfig(cfg, rawPlan, nil, "dev-example-project")[managedKeysKey]

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
			name: "unsets a key the declaration dropped",
			current: map[string]string{
				managedKeysKey:  "limits.cpu,limits.memory,raw.idmap",
				"limits.cpu":    "8",
				"limits.memory": "16GiB",
			},
			yaml: planBase + "  config:\n    limits.cpu: \"8\"\n",
			want: []string{"limits.memory"},
		},
		{
			name: "leaves a key the user added by hand alone",
			current: map[string]string{
				managedKeysKey:     "limits.cpu,raw.idmap",
				"security.nesting": "true", // not managed by idev
			},
			yaml: planBase + "  config:\n    limits.cpu: \"8\"\n",
			want: nil,
		},
		{
			name:    "with no record, only idmap is followed",
			current: map[string]string{idmapConfigKey: "uid 1000 0"},
			yaml:    planBase + "workspace:\n  idmap: shift\n",
			want:    []string{idmapConfigKey},
		},
		{
			name:    "with neither a record nor a difference, nothing happens",
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

			got := staleConfigKeys(tt.current, desiredConfig(cfg, plan, nil, "dev-example-project"), plan)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("staleConfigKeys() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// For devices too, only what idev created is followed on removal.
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
			"gone":      {"type": "disk"}, // dropped from the declaration
			"manual":    {"type": "nic"},  // added by the user, by hand
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
	got := desiredConfig(cfg, rawPlan, nil, "dev-example-project")[managedDevicesKey]

	if got != "data,workspace" {
		t.Errorf("%s = %q", managedDevicesKey, got)
	}
}

// Several checkouts can live on one machine (spec 05-incus.md 5.1).
func TestInstanceNameScope(t *testing.T) {
	tests := []struct {
		name   string
		scope  string
		root   string
		branch string
		want   string
	}{
		{"the name alone by default", "", "/home/u/a", "main", "dev-example-project"},
		{"name", "name", "/home/u/a", "main", "dev-example-project"},
		{"branch", "branch", "/home/u/a", "feature/x", "dev-example-project-feature-x"},
		{"branch, on the default branch", "branch", "/home/u/a", "main", "dev-example-project-main"},
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

// The path scope gives a different name per checkout.
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
		t.Errorf("different checkouts got the same name: %q", a)
	}
	if a != name("/home/u/checkout-a") {
		t.Error("want the same path to give the same name")
	}
	for _, got := range []string{a, b} {
		if !strings.HasPrefix(got, "dev-example-project-") {
			t.Errorf("name = %q", got)
		}
	}
}

// When the branch cannot be read, the error says what to do.
func TestInstanceNameScopeBranchError(t *testing.T) {
	cfg := mustParse(t, planBase)
	cfg.Project.Scope = config.ScopeBranch

	_, err := instanceNameFor(cfg, func() (string, error) { return "", errors.New("not a git repository") })
	if err == nil || !strings.Contains(err.Error(), "branch") {
		t.Errorf("error = %v", err)
	}
}

// Reading the branch name works in a repository with no commits, and on a
// detached HEAD.
// The reason git failed is the whole diagnosis: not installed, not a
// repository and exited non-zero send the user to three different places.
func TestGitBranchWithNothingToReport(t *testing.T) {
	// Both commands succeed and print nothing: there is no git failure to
	// pass on, so the message must not pretend to have one.
	fake := &runnertest.Fake{Stdout: map[string]string{"git -C /r": "\n"}}

	_, err := gitBranch(context.Background(), fake, "/r")()
	if err == nil {
		t.Fatal("gitBranch() = nil error, want one")
	}
	if want := "could not determine the git branch of /r"; err.Error() != want {
		t.Errorf("gitBranch() error = %q, want %q", err, want)
	}
}

func TestGitBranchSaysWhyItFailed(t *testing.T) {
	fake := &runnertest.Fake{Err: map[string]error{
		"git -C /r": errors.New("exec: \"git\": executable file not found in $PATH"),
	}}

	_, err := gitBranch(context.Background(), fake, "/r")()
	if err == nil {
		t.Fatal("gitBranch() = nil error, want one")
	}
	if !strings.Contains(err.Error(), "executable file not found") {
		t.Errorf("gitBranch() error = %q, want the reason git failed", err)
	}
}

func TestGitBranch(t *testing.T) {
	tests := []struct {
		name    string
		fake    *runnertest.Fake
		want    string
		wantErr bool
	}{
		{
			name: "an ordinary branch",
			fake: &runnertest.Fake{Stdout: map[string]string{"git -C /r symbolic-ref": "feature/x\n"}},
			want: "feature/x",
		},
		{
			name: "a commit hash on a detached HEAD",
			fake: &runnertest.Fake{
				Err:    map[string]error{"git -C /r symbolic-ref": errors.New("not a symbolic ref")},
				Stdout: map[string]string{"git -C /r rev-parse": "a8f213\n"},
			},
			want: "a8f213",
		},
		{
			name:    "not a Git repository",
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

// With the user managing the idmap, idev sets no shift of its own.
//
// ApplyDevices replaces a declared device, so leaving the key out is what
// clears a shift idev set earlier — which it must, since shift=true beside the
// user's own raw.idmap maps the workspace twice.
func TestDesiredDevicesClearsShiftWhenTheUserTakesOver(t *testing.T) {
	cfg := mustParse(t, planBase+"  config:\n    raw.idmap: \"both 1000 0\"\n")

	plan, err := resolveIDMap(cfg, 1000, 1000, func(int, int) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if plan.Managed {
		t.Fatal("resolveIDMap() says idev manages the mapping, want the user's own to win")
	}

	got := desiredDevices(cfg, plan, "dev-example-project")

	if shift, ok := got["workspace"]["shift"]; ok {
		t.Errorf("workspace shift = %q, want idev to set none", shift)
	}
}

// A recorded key the instance does not actually have is not stale: unsetting
// it would make up report a change on a run that changed nothing.
func TestStaleConfigKeysIgnoresWhatIsAlreadyGone(t *testing.T) {
	current := map[string]string{
		managedProjectKey: "example-project",
		managedKeysKey:    "limits.memory,security.nesting",
		"limits.memory":   "8GiB",
		// security.nesting is recorded but no longer on the instance.
	}

	got := staleConfigKeys(current, map[string]string{}, idmapPlan{})

	if diff := cmp.Diff([]string{"limits.memory"}, got); diff != "" {
		t.Errorf("staleConfigKeys() mismatch (-want +got):\n%s", diff)
	}
}

// Unsetting a key the instance never had changes nothing, so it must not be
// reported as needing a restart.
func TestRestartOwedIgnoresUnsettingWhatIsAbsent(t *testing.T) {
	_, all, _ := restartOwed(true, map[string]string{}, map[string]string{},
		[]string{"security.nesting"}, time.Time{})

	if len(all) != 0 {
		t.Errorf("restartOwed() = %v, want none for a key that was not set", all)
	}
}

// The record is written by idev but can be edited by hand. An entry that is
// not pool/name names no volume, and acting on it would delete the wrong
// thing.
func TestSplitVolumeRejectsAHalfReference(t *testing.T) {
	for _, ref := range []string{"/x", "x/", "/", "plain"} {
		if _, _, ok := splitVolume(ref); ok {
			t.Errorf("splitVolume(%q) = ok, want it rejected", ref)
		}
	}
	pool, name, ok := splitVolume("default/cache")
	if !ok || pool != "default" || name != "cache" {
		t.Errorf("splitVolume(default/cache) = %q, %q, %v", pool, name, ok)
	}
}

// shift belongs on a mount of a host path. A disk with neither source nor
// pool mounts nothing, so writing it there would be meaningless.
func TestDesiredDevicesLeavesASourcelessDiskAlone(t *testing.T) {
	cfg := mustParse(t, planBase+`
  devices:
    root:
      type: disk
      path: /mnt
`)
	devices := desiredDevices(cfg, idmapPlan{Mode: config.IDMapShift, Managed: true}, "dev-example-project")

	if _, ok := devices["root"]["shift"]; ok {
		t.Errorf("root device = %v, want no shift on a disk that mounts no host path", devices["root"])
	}
}

// A stopped instance applies everything when it starts, so nothing is owed —
// including anything an earlier run left owed.
func TestRestartOwedIsEmptyForAStoppedInstance(t *testing.T) {
	before := map[string]string{
		managedRestartKey: recordRestart(time.Now(),
			map[string]bootedValue{"security.nesting": {value: "false", known: true}}),
	}

	fresh, all, owed := restartOwed(false, before,
		map[string]string{"security.nesting": "true"}, nil, time.Time{})

	if fresh != nil || all != nil || owed != nil {
		t.Errorf("restartOwed() = %v, %v, %v; want nothing owed", fresh, all, owed)
	}
}

// raw.idmap changed spelling, not meaning, so sameIDMapping decides whether a
// restart is owed. Getting it wrong costs in both directions: too loose and an
// owed restart is skipped while up reports ready, too strict and a spurious
// one kills whatever the user is running inside the container.
func TestSameIDMapping(t *testing.T) {
	tests := []struct {
		name       string
		want, have string
		same       bool
	}{
		{"the same text", "both 1000 0", "both 1000 0", true},
		{"both expands to uid and gid", "uid 1000 0\ngid 1000 0", "both 1000 0", true},
		{"order does not matter", "gid 1000 0\nuid 1000 0", "both 1000 0", true},
		// A value written by anything but idev may space its fields
		// differently; that is not a change worth a restart.
		{"whitespace inside a line does not matter", "uid  1000   0", "uid 1000 0", true},
		{"a trailing newline does not matter", "both 1000 0\n", "both 1000 0", true},
		// A line idev does not write is compared trimmed, so indentation in
		// a hand-edited value is not a difference.
		{"space around a line idev did not write", "  keep me  ", "keep me", true},
		{"a different id is a different mapping", "both 1001 0", "both 1000 0", false},
		// A "both" line with anything after it is not idev's shape, so it is
		// compared whole rather than expanded from its first three fields.
		{"a longer both line is not truncated", "both 1000 0 junk", "both 1000 0", false},
		{"an extra line is a difference", "uid 1000 0\ngid 1000 0", "uid 1000 0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameIDMapping(idmapConfigKey, tt.want, tt.have); got != tt.same {
				t.Errorf("sameIDMapping(%q, %q) = %v, want %v", tt.want, tt.have, got, tt.same)
			}
		})
	}

	// Only raw.idmap is normalised; another key is compared literally.
	if sameIDMapping("security.nesting", "true ", "true") {
		t.Error("sameIDMapping normalised a key that is not raw.idmap")
	}
}

// The two wordings are the only thing telling a user whether this run caused
// the pending restart or whether they are looking at one left over, so which
// one appears is part of the behaviour.
func TestRestartWarningWording(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	before := map[string]string{
		managedRestartKey: recordRestart(started,
			map[string]bootedValue{"security.nesting": {value: "false", known: true}}),
		"security.nesting": "true",
	}
	desired := map[string]string{"security.nesting": "true"}

	// Nothing changed this run: it is carried forward from an earlier one.
	fresh, all, _ := restartOwed(true, before, desired, nil, started)
	if len(fresh) != 0 {
		t.Errorf("fresh = %v, want none: this run changed nothing", fresh)
	}
	if got := restartWarning(fresh, all); !strings.Contains(got, "still waiting on a restart") {
		t.Errorf("warning = %q, want the carried-forward wording", got)
	}

	// And a run that changes it again says so instead: the record still
	// carries the booted value, but the stored value is not what dev.yml now
	// asks for.
	before["security.nesting"] = "false"
	fresh, all, _ = restartOwed(true, before, desired, nil, started)
	if len(fresh) == 0 {
		t.Fatal("fresh = none, want the change reported")
	}
	if got := restartWarning(fresh, all); !strings.Contains(got, "changed but the instance is running") {
		t.Errorf("warning = %q, want the changed wording", got)
	}
}
