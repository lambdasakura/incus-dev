package cli

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lambdasakura/incus-dev/internal/config"
)

// resolveSecrets はホスト側から秘密情報を取り込む（仕様 03-configuration.md 3.12）。
//
// dev.yml はGitへcommitされる前提なので値そのものは書かない。
// 取得できないものがあれば、どれが足りないかをまとめて報告する。
func resolveSecrets(cfg *config.Config, lookupEnv func(string) (string, bool)) (map[string]string, error) {
	if len(cfg.Secrets) == 0 {
		return nil, nil
	}

	out := make(map[string]string, len(cfg.Secrets))
	var missing []string

	for _, name := range slices.Sorted(maps.Keys(cfg.Secrets)) {
		secret := cfg.Secrets[name]

		value, err := readSecret(secret, lookupEnv)
		switch {
		case err == nil:
			out[name] = value
		case secret.Optional:
			continue
		default:
			missing = append(missing, fmt.Sprintf("%s (%s): %v", name, secret.Source(), err))
		}
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("cannot resolve secret(s):\n  %s", strings.Join(missing, "\n  "))
	}
	return out, nil
}

// readSecret は1件の秘密情報を取得する。
func readSecret(secret config.Secret, lookupEnv func(string) (string, bool)) (string, error) {
	if secret.Env != "" {
		value, ok := lookupEnv(secret.Env)
		if !ok {
			return "", fmt.Errorf("not set")
		}
		return value, nil
	}

	path, err := expandHome(secret.File)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path) //nolint:gosec // 利用者が指定したファイルを読むことが目的
	if err != nil {
		return "", err
	}
	// ファイル末尾の改行は値に含めない。
	return strings.TrimSpace(string(data)), nil
}

// expandHome は先頭の ~ をホームディレクトリへ展開する。
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/")), nil
}
