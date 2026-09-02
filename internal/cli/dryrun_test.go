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

	got := strings.Join(planActions(cfg, "dev-example-project", nil, plan), "\n")

	for _, want := range []string{
		"Create instance dev-example-project (images:ubuntu/24.04)",
		"Apply profiles: default",
		"Set config limits.cpu=8",
		"Add device workspace (disk /home/u/src/example -> /workspace)",
		"Set devkit markers (user.incus-devkit.*)",
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

	got := strings.Join(planActions(cfg, "dev-example-project", current, plan), "\n")

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
}

func TestPlanActionsStartsStoppedInstance(t *testing.T) {
	cfg := mustParse(t, planBase)
	current := &incus.Instance{Name: "dev-example-project", Status: "Stopped"}

	got := strings.Join(planActions(cfg, "dev-example-project", current, idmapPlan{}), "\n")

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
			got := strings.Join(planActions(mustParse(t, tt.yaml), "dev-x", nil, idmapPlan{}), "\n")

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
	got := strings.Join(planActions(cfg, "dev-x", nil, idmapPlan{}), "\n")

	if !strings.Contains(got, "Apply no profiles") {
		t.Errorf("plan =\n%s", got)
	}
}
