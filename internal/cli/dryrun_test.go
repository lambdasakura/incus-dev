package cli

import (
	"strings"
	"testing"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/incus"
)

const dryRunYAML = planBase + `
  config:
    limits.cpu: "8"
provision:
  - name: prepare
    run: "true"
  - name: main playbook
    ansible:
      playbook: .incus-dev/ansible/site.yml
`

func TestPlanActionsForNewInstance(t *testing.T) {
	cfg := mustParse(t, dryRunYAML)
	plan := idmapPlan{Mode: config.IDMapShift, Managed: true}

	got := strings.Join(planActions(cfg, "dev-example-project", nil, plan, nil), "\n")

	for _, want := range []string{
		"Create instance dev-example-project (images:ubuntu/24.04)",
		"Apply profiles: default",
		"Set config limits.cpu=8",
		"Add device workspace (disk /home/u/src/example -> /workspace)",
		"Set idev markers (user.incus-dev.*)",
		"Start instance",
		"Bootstrap: 1 step (default)",
		"Provision step 1/2: prepare (run)",
		"Provision step 2/2: main playbook (ansible",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plan =\n%s\nwant it to contain %q", got, want)
		}
	}
}

func TestPlanActionsForExistingInstance(t *testing.T) {
	cfg := mustParse(t, dryRunYAML)
	plan := idmapPlan{Mode: config.IDMapShift, Managed: true}

	current := &incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			"limits.cpu":      "4",          // changed
			idmapConfigKey:    "uid 1000 0", // unset, since we switch to shift
		},
		Devices: map[string]incus.Device{
			"workspace": {"type": "disk", "path": "/workspace", "source": "/old"},
		},
	}

	got := strings.Join(planActions(cfg, "dev-example-project", current, plan, nil), "\n")

	for _, want := range []string{
		"Use existing instance dev-example-project (Running)",
		"Set config limits.cpu=8",
		"Unset config raw.idmap",
		"Update device workspace",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plan =\n%s\nwant it to contain %q", got, want)
		}
	}
	for _, unwanted := range []string{"Create instance", "Start instance"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("plan =\n%s\nwant it not to contain %q", got, unwanted)
		}
	}
	// A preflight that lists work it will not do is as misleading as one that
	// hides work it will: the device above is the only one that changes.
	if n := strings.Count(got, "Update device"); n != 1 {
		t.Errorf("plan =\n%s\nwant exactly one device update, got %d", got, n)
	}
}

// A device whose values already match is not reported as an update.
func TestPlanActionsLeavesAnUnchangedDeviceAlone(t *testing.T) {
	cfg := mustParse(t, planBase)
	plan := idmapPlan{Mode: config.IDMapShift, Managed: true}

	current := &incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{managedProjectKey: "example-project"},
		Devices: map[string]incus.Device{
			config.WorkspaceDeviceName: desiredDevices(cfg, plan, "dev-example-project")[config.WorkspaceDeviceName],
		},
	}

	got := strings.Join(planActions(cfg, "dev-example-project", current, plan, nil), "\n")

	if strings.Contains(got, "Update device") {
		t.Errorf("plan =\n%s\nwant no update for a device that already matches", got)
	}
}

// The plan shows what up would remove, not only what it would set.
//
// Removing config and devices is the one destructive thing up does to an
// existing instance, so a preflight that hides it is worse than none
// (spec 04-cli.md 4.8).
func TestPlanActionsShowsRemovals(t *testing.T) {
	cfg := mustParse(t, planBase)
	plan := idmapPlan{Mode: config.IDMapRaw, Managed: true, UID: 1000, GID: 1000}

	current := &incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey:  "example-project",
			managedKeysKey:     "limits.memory,security.nesting",
			managedDevicesKey:  "extdata,workspace",
			"limits.memory":    "8GiB", // idev applied it; the declaration dropped it
			"security.nesting": "true", // the same
		},
		Devices: map[string]incus.Device{
			"workspace": {"type": "disk", "path": "/workspace", "source": "/home/u/src/example"},
			"extdata":   {"type": "disk", "path": "/data", "source": "/srv/data"},
		},
	}

	got := strings.Join(planActions(cfg, "dev-example-project", current, plan, nil), "\n")

	for _, want := range []string{
		"Unset config limits.memory",
		"Unset config security.nesting",
		"Remove device extdata",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plan =\n%s\nwant it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "Remove device workspace") {
		t.Errorf("plan =\n%s\nwant the declared device kept", got)
	}
}

// What the user set by hand is not idev's to remove, so the plan must not
// claim it will be.
func TestPlanActionsLeavesUnrecordedConfigAlone(t *testing.T) {
	cfg := mustParse(t, planBase)
	plan := idmapPlan{Mode: config.IDMapShift, Managed: true}

	current := &incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedKeysKey:    "limits.cpu",
			idmapConfigKey:    "uid 1000 0", // set by hand, not recorded
		},
	}

	got := strings.Join(planActions(cfg, "dev-example-project", current, plan, nil), "\n")

	if strings.Contains(got, "Unset config "+idmapConfigKey) {
		t.Errorf("plan =\n%s\nwant a key idev did not set left alone", got)
	}
}

func TestPlanActionsStartsStoppedInstance(t *testing.T) {
	cfg := mustParse(t, planBase)
	current := &incus.Instance{Name: "dev-example-project", Status: "Stopped"}

	got := strings.Join(planActions(cfg, "dev-example-project", current, idmapPlan{}, nil), "\n")

	if !strings.Contains(got, "Start instance") {
		t.Errorf("plan =\n%s\nwant it to show a start when it is stopped", got)
	}
}

func TestPlanActionsBootstrapSource(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"default", dryRunYAML, "Bootstrap: 1 step (default)"},
		{"declared", planBase + "bootstrap:\n  - run: a\n  - run: b\n", "Bootstrap: 2 step(s) (from dev.yml)"},
		{"disabled", planBase + "bootstrap: []\n", "Bootstrap: skipped"},
		{"not needed", planBase + "provision:\n  - run: a\n", "Bootstrap: skipped"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.Join(planActions(mustParse(t, tt.yaml), "dev-x", nil, idmapPlan{}, nil), "\n")

			if !strings.Contains(got, tt.want) {
				t.Errorf("plan =\n%s\nwant it to contain %q", got, tt.want)
			}
		})
	}
}

func TestPlanActionsNoProfiles(t *testing.T) {
	cfg := mustParse(t, planBase+`
  profiles: []
  devices:
    root:
      type: disk
      pool: default
      path: /
`)
	got := strings.Join(planActions(cfg, "dev-x", nil, idmapPlan{}, nil), "\n")

	if !strings.Contains(got, "Apply no profiles") {
		t.Errorf("plan =\n%s", got)
	}
}

// A device whose type changes is replaced wholesale by up, so the preview has
// to say so. Excluding type reported nothing at all when it was the only
// difference.
func TestPlanActionsReportsAChangedDeviceType(t *testing.T) {
	cfg := mustParse(t, planBase+`
  devices:
    extra:
      type: disk
      source: /srv/data
      path: /data
`)
	plan := idmapPlan{Mode: config.IDMapNone}

	current := &incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{managedProjectKey: "example-project"},
		Devices: map[string]incus.Device{
			config.WorkspaceDeviceName: desiredDevices(cfg, plan, "dev-example-project")[config.WorkspaceDeviceName],
			"extra":                    {"type": "none", "source": "/srv/data", "path": "/data"},
		},
	}

	got := strings.Join(planActions(cfg, "dev-example-project", current, plan, nil), "\n")

	if !strings.Contains(got, "Update device extra (type=disk)") {
		t.Errorf("plan =\n%s\nwant the type change reported", got)
	}
}
