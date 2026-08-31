package cli

import (
	"errors"
	"strings"
	"testing"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
)

var errNotPermitted = errors.New("subuid is not configured")

func TestResolveIDMapMode(t *testing.T) {
	permitted := func(int, int) error { return nil }
	denied := func(int, int) error { return errNotPermitted }

	tests := []struct {
		name      string
		declared  config.IDMapMode
		check     func(int, int) error
		want      config.IDMapMode
		wantWarn  bool
		wantError bool
	}{
		{
			name:     "auto: 許可されていればraw",
			declared: config.IDMapAuto,
			check:    permitted,
			want:     config.IDMapRaw,
		},
		{
			// ホストへ手を入れなくても動くことを優先する
			name:     "auto: 許可されていなければshiftへ退避",
			declared: config.IDMapAuto,
			check:    denied,
			want:     config.IDMapShift,
			wantWarn: true,
		},
		{
			name:      "raw: 許可されていなければ失敗",
			declared:  config.IDMapRaw,
			check:     denied,
			wantError: true,
		},
		{
			name:     "raw: 許可されていればraw",
			declared: config.IDMapRaw,
			check:    permitted,
			want:     config.IDMapRaw,
		},
		{
			name:     "shift: 検査しない",
			declared: config.IDMapShift,
			check:    denied,
			want:     config.IDMapShift,
		},
		{
			name:     "none: 検査しない",
			declared: config.IDMapNone,
			check:    denied,
			want:     config.IDMapNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, warn, err := resolveIDMapMode(tt.declared, tt.check)

			if tt.wantError {
				if err == nil {
					t.Fatal("resolveIDMapMode() = nil error, want error")
				}
				if !strings.Contains(err.Error(), "subuid") {
					t.Errorf("error = %q", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveIDMapMode() error = %v", err)
			}
			if mode != tt.want {
				t.Errorf("mode = %q, want %q", mode, tt.want)
			}
			if (warn != "") != tt.wantWarn {
				t.Errorf("warn = %q, wantWarn = %v", warn, tt.wantWarn)
			}
		})
	}
}

// 退避した場合の警告は、理由と最適な設定方法を示すこと
func TestFallbackWarningIsActionable(t *testing.T) {
	_, warn, err := resolveIDMapMode(config.IDMapAuto, func(int, int) error { return errNotPermitted })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"shift", "root:"} {
		if !strings.Contains(warn, want) {
			t.Errorf("warn = %q, %q を含むこと", warn, want)
		}
	}
}

func TestPlanUsesResolvedIDMapMode(t *testing.T) {
	cfg := mustParse(t, planBase)

	t.Run("raw", func(t *testing.T) {
		if got := desiredConfig(cfg, config.IDMapRaw, 1000, 1001)[idmapConfigKey]; got != "uid 1000 0\ngid 1001 0" {
			t.Errorf("raw.idmap = %q", got)
		}
		if got := desiredDevices(cfg, config.IDMapRaw)["workspace"]["shift"]; got != "false" {
			t.Errorf("shift = %q, rawではshiftを明示的に無効化すること", got)
		}
	})

	t.Run("shift", func(t *testing.T) {
		if got, ok := desiredConfig(cfg, config.IDMapShift, 1000, 1000)[idmapConfigKey]; ok {
			t.Errorf("raw.idmap = %q, shiftではraw.idmapを設定しないこと", got)
		}
		if got := desiredDevices(cfg, config.IDMapShift)["workspace"]["shift"]; got != "true" {
			t.Errorf("workspace device shift = %q, want true", got)
		}
	})

	t.Run("none", func(t *testing.T) {
		if _, ok := desiredConfig(cfg, config.IDMapNone, 1000, 1000)[idmapConfigKey]; ok {
			t.Error("noneではraw.idmapを設定しないこと")
		}
		if got := desiredDevices(cfg, config.IDMapNone)["workspace"]["shift"]; got != "false" {
			t.Errorf("shift = %q, noneではshiftを明示的に無効化すること", got)
		}
	})
}

// idmap方式を切り替えたとき、devkitが設定した古いキーを残さない
func TestStaleIDMapKeys(t *testing.T) {
	cfg := mustParse(t, planBase)
	withRaw := map[string]string{idmapConfigKey: "uid 1000 0"}

	tests := []struct {
		name    string
		cfg     *config.Config
		current map[string]string
		mode    config.IDMapMode
		want    []string
	}{
		{"shiftへ切り替え", cfg, withRaw, config.IDMapShift, []string{idmapConfigKey}},
		{"noneへ切り替え", cfg, withRaw, config.IDMapNone, []string{idmapConfigKey}},
		{"rawのまま", cfg, withRaw, config.IDMapRaw, nil},
		{"元々設定が無い", cfg, map[string]string{}, config.IDMapShift, nil},
		{
			name:    "利用者が明示したキーには触れない",
			cfg:     mustParse(t, planBase+"  config:\n    raw.idmap: \"both 1234 0\"\n"),
			current: withRaw,
			mode:    config.IDMapShift,
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := staleIDMapKeys(tt.cfg, tt.current, tt.mode)

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

// ホストのディレクトリをマウントする追加deviceにも、解決したidmap方式を伝播させる。
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
			if got := desiredDevices(cfg, tt.mode)["extdata"]["shift"]; got != tt.want {
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
		if got := desiredDevices(cfg, mode)["extdata"]["shift"]; got != "true" {
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
	devices := desiredDevices(cfg, config.IDMapShift)

	for _, name := range []string{"root", "volume", "eth0", "gpu0"} {
		if got, ok := devices[name]["shift"]; ok {
			t.Errorf("%s の shift = %q, ホストのパスを指すdisk以外には付けないこと", name, got)
		}
	}
}
