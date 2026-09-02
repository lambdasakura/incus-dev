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

// resolveSecrets reads the secrets from the host (spec 03-configuration.md
// 3.12).
//
// dev.yml is meant to be committed, so the values are never written in it.
// Whatever cannot be read is reported together, naming what is missing.
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

// readSecret reads one secret.
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
	data, err := os.ReadFile(path) //nolint:gosec // reading the file the user named is the point
	if err != nil {
		return "", err
	}
	// A trailing newline is not part of the value.
	return strings.TrimSpace(string(data)), nil
}

// expandHome expands a leading ~ to the home directory.
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
