package provision_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/provision"
)

func selectSteps(t *testing.T, yaml string, sel provision.Selection) []int {
	t.Helper()

	cfg := parseConfig(t, yaml)
	got, err := provision.Select(cfg.Provision, sel)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	return got
}

const threeSteps = base + `
provision:
  - name: base packages
    run: "true"
  - name: provision
    ansible:
      playbook: p.yml
  - run: "true"
`

func TestSelectAllByDefault(t *testing.T) {
	got := selectSteps(t, threeSteps, provision.Selection{})

	if diff := cmp.Diff([]int{0, 1, 2}, got); diff != "" {
		t.Errorf("Select() mismatch (-want +got):\n%s", diff)
	}
}

func TestSelectOnly(t *testing.T) {
	tests := []struct {
		name string
		only []string
		want []int
	}{
		{"名前で指定", []string{"provision"}, []int{1}},
		{"番号で指定", []string{"2"}, []int{1}},
		{"複数指定", []string{"base packages", "3"}, []int{0, 2}},
		{"名前の無いステップを番号で", []string{"step 3"}, []int{2}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectSteps(t, threeSteps, provision.Selection{Only: tt.only})

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Select() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSelectFrom(t *testing.T) {
	tests := []struct {
		name string
		from string
		want []int
	}{
		{"名前で指定", "provision", []int{1, 2}},
		{"番号で指定", "1", []int{0, 1, 2}},
		{"最後のステップ", "3", []int{2}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectSteps(t, threeSteps, provision.Selection{From: tt.from})

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Select() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// 同じ名前のステップが複数ある場合は、すべて選ぶ
func TestSelectMatchesAllWithSameName(t *testing.T) {
	got := selectSteps(t, base+`
provision:
  - name: setup
    run: "true"
  - name: other
    run: "true"
  - name: setup
    run: "true"
`, provision.Selection{Only: []string{"setup"}})

	if diff := cmp.Diff([]int{0, 2}, got); diff != "" {
		t.Errorf("Select() mismatch (-want +got):\n%s", diff)
	}
}

func TestSelectErrors(t *testing.T) {
	tests := []struct {
		name string
		sel  provision.Selection
		want []string // エラーに含まれるべき文字列
	}{
		{
			name: "存在しない名前",
			sel:  provision.Selection{Only: []string{"nope"}},
			want: []string{"nope", "base packages", "provision"},
		},
		{
			name: "範囲外の番号",
			sel:  provision.Selection{Only: []string{"9"}},
			want: []string{"9"},
		},
		{
			name: "fromが存在しない",
			sel:  provision.Selection{From: "nope"},
			want: []string{"nope"},
		},
		{
			name: "onlyとfromの併用",
			sel:  provision.Selection{Only: []string{"provision"}, From: "provision"},
			want: []string{"only", "from"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := parseConfig(t, threeSteps)

			_, err := provision.Select(cfg.Provision, tt.sel)
			if err == nil {
				t.Fatal("Select() = nil error, want error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, %q を含むこと", err.Error(), want)
				}
			}
		})
	}
}

// ステップが無い場合に指定するとエラーになる
func TestSelectWithoutSteps(t *testing.T) {
	cfg := parseConfig(t, base)

	if _, err := provision.Select(cfg.Provision, provision.Selection{Only: []string{"x"}}); err == nil {
		t.Error("Select() = nil error, want error")
	}
	if got, err := provision.Select(cfg.Provision, provision.Selection{}); err != nil || len(got) != 0 {
		t.Errorf("Select() = %v, %v, 指定が無ければ空でよい", got, err)
	}
}
