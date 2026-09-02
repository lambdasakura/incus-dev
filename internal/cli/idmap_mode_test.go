package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/lambdasakura/incus-dev/internal/config"
)

var errNotPermitted = errors.New("subuid is not configured")

func permitted(int, int) error { return nil }
func denied(int, int) error    { return errNotPermitted }

func TestResolveIDMap(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		check       func(int, int) error
		wantManaged bool
		wantMode    config.IDMapMode
		wantWarn    bool
		wantErr     bool
	}{
		{
			name:        "auto: raw when it is permitted",
			yaml:        planBase,
			check:       permitted,
			wantManaged: true,
			wantMode:    config.IDMapRaw,
		},
		{
			// Working without the host being touched takes priority.
			name:        "auto: falls back to shift when it is not permitted",
			yaml:        planBase,
			check:       denied,
			wantManaged: true,
			wantMode:    config.IDMapShift,
			wantWarn:    true,
		},
		{
			name:    "raw: fails when it is not permitted",
			yaml:    planBase + "workspace:\n  idmap: raw\n",
			check:   denied,
			wantErr: true,
		},
		{
			name:        "raw: raw when it is permitted",
			yaml:        planBase + "workspace:\n  idmap: raw\n",
			check:       permitted,
			wantManaged: true,
			wantMode:    config.IDMapRaw,
		},
		{
			name:        "shift: nothing is checked",
			yaml:        planBase + "workspace:\n  idmap: shift\n",
			check:       denied,
			wantManaged: true,
			wantMode:    config.IDMapShift,
		},
		{
			name:        "none: nothing is checked",
			yaml:        planBase + "workspace:\n  idmap: none\n",
			check:       denied,
			wantManaged: true,
			wantMode:    config.IDMapNone,
		},
		{
			name:        "stays out of the way when the user set raw.idmap",
			yaml:        planBase + "  config:\n    raw.idmap: \"both 1234 0\"\n",
			check:       denied,
			wantManaged: false,
		},
		{
			// With both written, workspace.idmap has no effect, so say so.
			name:        "warns when raw.idmap and workspace.idmap are both set",
			yaml:        planBase + "  config:\n    raw.idmap: \"both 1234 0\"\nworkspace:\n  idmap: shift\n",
			check:       denied,
			wantManaged: false,
			wantWarn:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := resolveIDMap(mustParse(t, tt.yaml), 1000, 1001, tt.check)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveIDMap() = %+v, want error", plan)
				}
				if !strings.Contains(err.Error(), "subuid") {
					t.Errorf("error = %q", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveIDMap() error = %v", err)
			}
			if plan.Managed != tt.wantManaged {
				t.Errorf("Managed = %v, want %v", plan.Managed, tt.wantManaged)
			}
			if tt.wantManaged && plan.Mode != tt.wantMode {
				t.Errorf("Mode = %q, want %q", plan.Mode, tt.wantMode)
			}
			if (plan.Warning != "") != tt.wantWarn {
				t.Errorf("Warning = %q, wantWarn = %v", plan.Warning, tt.wantWarn)
			}
		})
	}
}

// The fallback warning gives the reason and the better configuration.
func TestFallbackWarningIsActionable(t *testing.T) {
	plan, err := resolveIDMap(mustParse(t, planBase), 1000, 1001, denied)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"shift", "root:1000:1", "root:1001:1", "/etc/subuid", "/etc/subgid"} {
		if !strings.Contains(plan.Warning, want) {
			t.Errorf("warning = %q, want it to contain %q", plan.Warning, want)
		}
	}
}

// On a host where the uid and gid differ, each is mapped correctly.
func TestRawIDMapUsesBothIDs(t *testing.T) {
	plan := idmapPlan{Mode: config.IDMapRaw, Managed: true, UID: 1000, GID: 1001}

	if got, want := plan.rawIDMap(), "uid 1000 0\ngid 1001 0"; got != want {
		t.Errorf("rawIDMap() = %q, want %q", got, want)
	}

	for _, p := range []idmapPlan{
		{Mode: config.IDMapShift, Managed: true},
		{Mode: config.IDMapNone, Managed: true},
		{Mode: config.IDMapRaw, Managed: false},
	} {
		if got := p.rawIDMap(); got != "" {
			t.Errorf("%+v: rawIDMap() = %q, want empty", p, got)
		}
	}
}

func TestPlanUsesResolvedIDMap(t *testing.T) {
	cfg := mustParse(t, planBase)

	t.Run("raw", func(t *testing.T) {
		plan := idmapPlan{Mode: config.IDMapRaw, Managed: true, UID: 1000, GID: 1000}

		if got := desiredConfig(cfg, plan, nil, "dev-example-project")[idmapConfigKey]; !strings.HasPrefix(got, "uid 1000 0") {
			t.Errorf("raw.idmap = %q", got)
		}
		if got := desiredDevices(cfg, plan, "dev-example-project")["workspace"]["shift"]; got != "false" {
			t.Errorf("shift = %q, want raw to disable shift explicitly", got)
		}
	})

	t.Run("shift", func(t *testing.T) {
		plan := idmapPlan{Mode: config.IDMapShift, Managed: true}

		if got, ok := desiredConfig(cfg, plan, nil, "dev-example-project")[idmapConfigKey]; ok {
			t.Errorf("raw.idmap = %q, want it unset under shift", got)
		}
		if got := desiredDevices(cfg, plan, "dev-example-project")["workspace"]["shift"]; got != "true" {
			t.Errorf("shift = %q, want true", got)
		}
	})

	t.Run("none", func(t *testing.T) {
		plan := idmapPlan{Mode: config.IDMapNone, Managed: true}

		if _, ok := desiredConfig(cfg, plan, nil, "dev-example-project")[idmapConfigKey]; ok {
			t.Error("want raw.idmap unset under none")
		}
		if got := desiredDevices(cfg, plan, "dev-example-project")["workspace"]["shift"]; got != "false" {
			t.Errorf("shift = %q, want none to disable shift explicitly", got)
		}
	})

	// When the user manages it, idev sets no raw.idmap of its own — but it
	// still says shift is off, so one it set earlier does not survive.
	t.Run("managed by the user", func(t *testing.T) {
		plan := idmapPlan{Managed: false}

		if _, ok := desiredConfig(cfg, plan, nil, "dev-example-project")[idmapConfigKey]; ok {
			t.Error("want raw.idmap unset")
		}
		if got := desiredDevices(cfg, plan, "dev-example-project")["workspace"]["shift"]; got != "false" {
			t.Errorf("shift = %q, want it explicitly false", got)
		}
	})
}

// Switching idmap strategies leaves no key idev set behind.
func TestStaleIDMapKeys(t *testing.T) {
	withRaw := map[string]string{idmapConfigKey: "uid 1000 0"}

	tests := []struct {
		name    string
		current map[string]string
		plan    idmapPlan
		want    []string
	}{
		{"switch to shift", withRaw, idmapPlan{Mode: config.IDMapShift, Managed: true}, []string{idmapConfigKey}},
		{"switch to none", withRaw, idmapPlan{Mode: config.IDMapNone, Managed: true}, []string{idmapConfigKey}},
		{"still raw", withRaw, idmapPlan{Mode: config.IDMapRaw, Managed: true}, nil},
		{"nothing was set to begin with", map[string]string{}, idmapPlan{Mode: config.IDMapShift, Managed: true}, nil},
		{"managed by the user", withRaw, idmapPlan{Managed: false}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := staleIDMapKeys(tt.current, tt.plan)

			if len(got) != len(tt.want) {
				t.Fatalf("staleIDMapKeys() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("staleIDMapKeys()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// The resolved strategy reaches extra devices that mount a host directory too.
// Without that, the workspace is writable and the extra mounts are not.
func TestDesiredDevicesPropagatesShiftToHostMounts(t *testing.T) {
	cfg := mustParse(t, planBase+`
  devices:
    extdata:
      type: disk
      source: /srv/dataset
      path: /data
`)

	tests := []struct {
		mode config.IDMapMode
		want string
	}{
		{config.IDMapShift, "true"},
		{config.IDMapRaw, "false"},
		{config.IDMapNone, "false"},
	}
	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			plan := idmapPlan{Mode: tt.mode, Managed: true}

			if got := desiredDevices(cfg, plan, "dev-example-project")["extdata"]["shift"]; got != tt.want {
				t.Errorf("shift = %q, want %q", got, tt.want)
			}
		})
	}
}

// A shift the project set explicitly wins.
func TestDesiredDevicesRespectsExplicitShift(t *testing.T) {
	cfg := mustParse(t, planBase+`
  devices:
    extdata:
      type: disk
      source: /srv/dataset
      path: /data
      shift: "true"
`)

	for _, mode := range []config.IDMapMode{config.IDMapRaw, config.IDMapNone, config.IDMapShift} {
		plan := idmapPlan{Mode: mode, Managed: true}

		if got := desiredDevices(cfg, plan, "dev-example-project")["extdata"]["shift"]; got != "true" {
			t.Errorf("mode=%s: shift = %q, want the explicit setting left alone", mode, got)
		}
	}
}

// A device that points at no host path gets no shift.
func TestDesiredDevicesLeavesNonHostMountsAlone(t *testing.T) {
	cfg := mustParse(t, planBase+`
  devices:
    root:
      type: disk
      pool: default
      path: /
    volume:
      type: disk
      pool: default
      source: myvolume
      path: /vol
    eth0:
      type: nic
      network: incusbr0
    gpu0:
      type: gpu
`)
	devices := desiredDevices(cfg, idmapPlan{Mode: config.IDMapShift, Managed: true}, "dev-example-project")

	for _, name := range []string{"root", "volume", "eth0", "gpu0"} {
		if got, ok := devices[name]["shift"]; ok {
			t.Errorf("shift on %s = %q, want it only on a disk that points at a host path", name, got)
		}
	}
	// A storage volume's source is not resolved as a path.
	if got := devices["volume"]["source"]; got != "myvolume" {
		t.Errorf("source of volume = %q, want the volume name left alone", got)
	}
}
