package provision

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
)

// Selection は実行するステップの絞り込み。
//
// ステップ数が増えると全体の再実行が重くなるため、
// 反復中は一部だけを流せるようにする（仕様 04-cli.md 4.2）。
type Selection struct {
	// Only は実行するステップ。名前または番号（1始まり）で指定する。
	Only []string
	// From は実行を開始するステップ。以降すべてを実行する。
	From string
}

// IsZero は絞り込みが指定されていないかを返す。
func (s Selection) IsZero() bool {
	return len(s.Only) == 0 && s.From == ""
}

// Select は実行するステップの位置を返す。指定が無ければすべてを返す。
func Select(steps []config.Step, sel Selection) ([]int, error) {
	if len(sel.Only) > 0 && sel.From != "" {
		return nil, fmt.Errorf("only and from cannot be used together")
	}

	all := make([]int, len(steps))
	for i := range steps {
		all[i] = i
	}
	if sel.IsZero() {
		return all, nil
	}

	if sel.From != "" {
		matched, err := match(steps, sel.From)
		if err != nil {
			return nil, err
		}
		return all[matched[0]:], nil
	}

	var out []int
	for _, ref := range sel.Only {
		matched, err := match(steps, ref)
		if err != nil {
			return nil, err
		}
		out = append(out, matched...)
	}

	// 指定順ではなく宣言順で実行する。ステップは前から順に
	// 積み上げる前提で書かれているため。
	out = dedup(out)
	slices.Sort(out)

	return out, nil
}

// match は名前または番号に一致するステップの位置を返す。
func match(steps []config.Step, ref string) ([]int, error) {
	if n, err := strconv.Atoi(ref); err == nil {
		if n < 1 || n > len(steps) {
			return nil, fmt.Errorf("step %d is out of range (1-%d)%s", n, len(steps), available(steps))
		}
		return []int{n - 1}, nil
	}

	var out []int
	for i, s := range steps {
		if s.DisplayName(i+1) == ref {
			out = append(out, i)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no step named %q%s", ref, available(steps))
	}
	return out, nil
}

// available は選べるステップを列挙した案内を返す。
func available(steps []config.Step) string {
	if len(steps) == 0 {
		return "; this project declares no provision steps"
	}

	names := make([]string, 0, len(steps))
	for i, s := range steps {
		names = append(names, fmt.Sprintf("%d. %s", i+1, s.DisplayName(i+1)))
	}
	return "\navailable steps:\n  " + strings.Join(names, "\n  ")
}

func dedup(indices []int) []int {
	seen := make(map[int]bool, len(indices))
	out := make([]int, 0, len(indices))

	for _, i := range indices {
		if seen[i] {
			continue
		}
		seen[i] = true
		out = append(out, i)
	}
	return out
}
