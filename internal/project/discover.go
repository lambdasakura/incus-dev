// Package project locates the project root, the directory holding
// .incus-dev/dev.yml.
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const (
	// ConfigDir is the name of the project's configuration directory. It
	// matches config.ConfigDir, which the packages reading dev.yml use so
	// they need not depend on this one.
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

// isAbsent reports whether the path simply is not there.
//
// A file where .incus-dev/ would be gives ENOTDIR rather than ENOENT, and that
// is not a project either, so the search carries on upwards.
func isAbsent(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// Discover looks for .incus-dev/dev.yml from startDir upwards and returns
// the nearest project root.
func Discover(startDir string) (*Project, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return nil, fmt.Errorf("resolve start directory %q: %w", startDir, err)
	}
	// Without this the search would simply start above a directory that is not
	// there, and a mistyped -C would operate on an ancestor's project.
	if info, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("start directory %q: %w", startDir, err)
	} else if !info.IsDir() {
		return nil, fmt.Errorf("start directory %q: not a directory", startDir)
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
		case err != nil && !isAbsent(err):
			// The error already names the path and the operation.
			return nil, fmt.Errorf("look for the project root: %w", err)
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
