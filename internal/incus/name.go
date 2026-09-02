package incus

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	// InstanceNamePrefix は生成するinstance名の接頭辞。
	// コマンド名 idev とは独立しており、「開発環境用instance」を意味する。
	InstanceNamePrefix = "dev-"
	// maxInstanceNameLength はIncusのinstance名の最大長。
	maxInstanceNameLength = 63
)

// InstanceName はプロジェクト名からIncus instance名を生成する。
// Incusのinstance名制約（英数字とハイフン、63文字以内）に適合するよう正規化する。
func InstanceName(projectName string) string {
	var sb strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(projectName) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			sb.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && sb.Len() > 0 {
				sb.WriteByte('-')
				prevDash = true
			}
		}
	}
	normalized := strings.Trim(sb.String(), "-")
	if normalized == "" {
		// 英数字を含まない名前はすべて同じ結果になってしまうため、
		// 元の名前から短いハッシュを作って区別する。
		normalized = shortHash(projectName)
	}

	name := InstanceNamePrefix + normalized
	if len(name) > maxInstanceNameLength {
		name = name[:maxInstanceNameLength]
	}
	return strings.TrimRight(name, "-")
}

// shortHash は名前を区別するための短い16進文字列を返す。
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}
