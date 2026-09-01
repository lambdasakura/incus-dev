// Package project locates the project root, the directory holding
// .incus-dev/dev.yml.
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// ConfigDir is the name of the project's configuration directory.
	ConfigDir = ".incus-dev"
	// ConfigFile is the name of the environment definition file.
	ConfigFile = "dev.yml"
)

// ErrNotFound reports that no project root was found.
var ErrNotFound = errors.New("project root not found")

// Project is a discovered project.
type Project struct {
	// Root is the absolute path of the project root.
	Root string
	// ConfigPath is the absolute path of dev.yml.
	ConfigPath string
}

// Discover looks for .incus-dev/dev.yml from startDir upwards and returns
// the nearest project root.
func Discover(startDir string) (*Project, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return nil, fmt.Errorf("resolve start directory %q: %w", startDir, err)
	}
	// Normalise, in case the path contains a symlink (t.TempDir() often does).
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}

	// A directory with .incus-dev/ but no dev.yml, which is likely a mistake.
	var configDirWithoutFile string

	for {
		configPath := filepath.Join(dir, ConfigDir, ConfigFile)
		switch info, err := os.Stat(configPath); {
		case err == nil && !info.IsDir():
			return &Project{Root: dir, ConfigPath: configPath}, nil
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return nil, fmt.Errorf("stat %s: %w", configPath, err)
		}

		if configDirWithoutFile == "" {
			if info, err := os.Stat(filepath.Join(dir, ConfigDir)); err == nil && info.IsDir() {
				configDirWithoutFile = dir
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	if configDirWithoutFile != "" {
		return nil, fmt.Errorf("%w: %s exists but %s is missing",
			ErrNotFound,
			filepath.Join(configDirWithoutFile, ConfigDir),
			filepath.Join(ConfigDir, ConfigFile))
	}
	return nil, fmt.Errorf("%w: %s was not found in %s or any parent directory",
		ErrNotFound, filepath.Join(ConfigDir, ConfigFile), startDir)
}
