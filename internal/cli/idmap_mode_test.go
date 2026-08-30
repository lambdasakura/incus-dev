package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/lambdasakura/incus-dev/internal/config"
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
		if got := desiredConfig(cfg, config.IDMapRaw)[idmapConfigKey]; !strings.HasPrefix(got, "both ") {
			t.Errorf("raw.idmap = %q", got)
		}
		if got := desiredDevices(cfg, config.IDMapRaw)["workspace"]["shift"]; got != "" {
			t.Errorf("shift = %q, rawではshiftを使わないこと", got)
		}
	})

	t.Run("shift", func(t *testing.T) {
		if got, ok := desiredConfig(cfg, config.IDMapShift)[idmapConfigKey]; ok {
			t.Errorf("raw.idmap = %q, shiftではraw.idmapを設定しないこと", got)
		}
		if got := desiredDevices(cfg, config.IDMapShift)["workspace"]["shift"]; got != "true" {
			t.Errorf("workspace device shift = %q, want true", got)
		}
	})

	t.Run("none", func(t *testing.T) {
		if _, ok := desiredConfig(cfg, config.IDMapNone)[idmapConfigKey]; ok {
			t.Error("noneではraw.idmapを設定しないこと")
		}
		if got := desiredDevices(cfg, config.IDMapNone)["workspace"]["shift"]; got != "" {
			t.Errorf("shift = %q, noneではshiftを使わないこと", got)
		}
	})
}
