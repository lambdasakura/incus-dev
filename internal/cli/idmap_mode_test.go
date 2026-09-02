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
			name:        "auto: 許可されていればraw",
			yaml:        planBase,
			check:       permitted,
			wantManaged: true,
			wantMode:    config.IDMapRaw,
		},
		{
			// ホストへ手を入れなくても動くことを優先する
			name:        "auto: 許可されていなければshiftへ退避",
			yaml:        planBase,
			check:       denied,
			wantManaged: true,
			wantMode:    config.IDMapShift,
			wantWarn:    true,
		},
		{
			name:    "raw: 許可されていなければ失敗",
			yaml:    planBase + "workspace:\n  idmap: raw\n",
			check:   denied,
			wantErr: true,
		},
		{
			name:        "raw: 許可されていればraw",
			yaml:        planBase + "workspace:\n  idmap: raw\n",
			check:       permitted,
			wantManaged: true,
			wantMode:    config.IDMapRaw,
		},
		{
			name:        "shift: 検査しない",
			yaml:        planBase + "workspace:\n  idmap: shift\n",
			check:       denied,
			wantManaged: true,
			wantMode:    config.IDMapShift,
		},
		{
			name:        "none: 検査しない",
			yaml:        planBase + "workspace:\n  idmap: none\n",
			check:       denied,
			wantManaged: true,
			wantMode:    config.IDMapNone,
		},
		{
			name:        "利用者がraw.idmapを明示していれば介入しない",
			yaml:        planBase + "  config:\n    raw.idmap: \"both 1234 0\"\n",
			check:       denied,
			wantManaged: false,
		},
		{
			// 両方書かれた場合、workspace.idmap は効かないので伝える
			name:        "raw.idmapとworkspace.idmapの併記を警告する",
			yaml:        planBase + "  config:\n    raw.idmap: \"both 1234 0\"\nworkspace:\n  idmap: shift\n",
			check:       denied,
			wantManaged: false,
			wantWarn:    true,
		},
		{
			// raw.idmap も disk の shift もコンテナ固有の仕組み
			name:        "コンテナ以外では介入しない",
			yaml:        planBase + "  type: virtual-machine\n",
			check:       denied,
			wantManaged: false,
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

// 退避した場合の警告は、理由と最適な設定方法を示すこと
func TestFallbackWarningIsActionable(t *testing.T) {
	plan, err := resolveIDMap(mustParse(t, planBase), 1000, 1001, denied)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"shift", "root:1000:1", "root:1001:1", "/etc/subuid", "/etc/subgid"} {
		if !strings.Contains(plan.Warning, want) {
			t.Errorf("warning = %q, %q を含むこと", plan.Warning, want)
		}
	}
}

// uidとgidが異なるホストでも、それぞれを正しく写像すること
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

		if got := desiredConfig(cfg, plan)[idmapConfigKey]; !strings.HasPrefix(got, "uid 1000 0") {
			t.Errorf("raw.idmap = %q", got)
		}
		if got := desiredDevices(cfg, plan, "dev-example-project")["workspace"]["shift"]; got != "false" {
			t.Errorf("shift = %q, rawではshiftを明示的に無効化すること", got)
		}
	})

	t.Run("shift", func(t *testing.T) {
		plan := idmapPlan{Mode: config.IDMapShift, Managed: true}

		if got, ok := desiredConfig(cfg, plan)[idmapConfigKey]; ok {
			t.Errorf("raw.idmap = %q, shiftでは設定しないこと", got)
		}
		if got := desiredDevices(cfg, plan, "dev-example-project")["workspace"]["shift"]; got != "true" {
			t.Errorf("shift = %q, want true", got)
		}
	})

	t.Run("none", func(t *testing.T) {
		plan := idmapPlan{Mode: config.IDMapNone, Managed: true}

		if _, ok := desiredConfig(cfg, plan)[idmapConfigKey]; ok {
			t.Error("noneではraw.idmapを設定しないこと")
		}
		if got := desiredDevices(cfg, plan, "dev-example-project")["workspace"]["shift"]; got != "false" {
			t.Errorf("shift = %q, noneではshiftを明示的に無効化すること", got)
		}
	})

	// 利用者が管理している場合、devkitはconfigにもdeviceにも触れない
	t.Run("利用者管理", func(t *testing.T) {
		plan := idmapPlan{Managed: false}

		if _, ok := desiredConfig(cfg, plan)[idmapConfigKey]; ok {
			t.Error("raw.idmapを設定しないこと")
		}
		if got, ok := desiredDevices(cfg, plan, "dev-example-project")["workspace"]["shift"]; ok {
			t.Errorf("shift = %q, 設定しないこと", got)
		}
	})
}

// idmap方式を切り替えたとき、devkitが設定した古いキーを残さない
func TestStaleIDMapKeys(t *testing.T) {
	withRaw := map[string]string{idmapConfigKey: "uid 1000 0"}

	tests := []struct {
		name    string
		current map[string]string
		plan    idmapPlan
		want    []string
	}{
		{"shiftへ切り替え", withRaw, idmapPlan{Mode: config.IDMapShift, Managed: true}, []string{idmapConfigKey}},
		{"noneへ切り替え", withRaw, idmapPlan{Mode: config.IDMapNone, Managed: true}, []string{idmapConfigKey}},
		{"rawのまま", withRaw, idmapPlan{Mode: config.IDMapRaw, Managed: true}, nil},
		{"元々設定が無い", map[string]string{}, idmapPlan{Mode: config.IDMapShift, Managed: true}, nil},
		{"利用者が管理している", withRaw, idmapPlan{Managed: false}, nil},
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

// ホストのディレクトリをマウントする追加deviceにも、解決した方式を伝播させる。
// そうしないと、workspaceだけ書けて追加マウントは書けない状態になる。
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

// プロジェクトが shift を明示している場合は尊重する
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
			t.Errorf("mode=%s: shift = %q, 明示指定を上書きしないこと", mode, got)
		}
	}
}

// ホストのパスを指さないdeviceには shift を付けない
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
			t.Errorf("%s の shift = %q, ホストのパスを指すdisk以外には付けないこと", name, got)
		}
	}
	// storage volume の source はパスとして解決しない
	if got := devices["volume"]["source"]; got != "myvolume" {
		t.Errorf("volume の source = %q, ボリューム名を書き換えないこと", got)
	}
}
