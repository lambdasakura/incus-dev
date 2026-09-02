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
	// ShadowedWhy says what is wrong with Shadowed, so the caller warning
	// about it does not have to guess. Both reasons reach here.
	ShadowedWhy string
	// Shadowed is a nearer dev.yml that could not be used, if there was one.
	//
	// The search continues past it so a real project above is not hidden, but
	// silently using the parent's instance is not what someone standing in the
	// nearer directory expects.
	Shadowed string
}

// isAbsent reports whether the path simply is not there.
//
// A file where .incus-dev/ would be gives ENOTDIR rather than ENOENT, and that
// is not a project either, so the search carries on upwards.
func isAbsent(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// isDanglingLink reports whether the path is a symbolic link whose target is
// not there. Lstat does not follow the link, so it succeeds where Stat gave
// ENOENT.
func isDanglingLink(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
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
	var wrongKind, wrongKindWhy string

	for {
		configPath := filepath.Join(dir, ConfigDir, ConfigFile)
		switch info, err := os.Stat(configPath); {
		case err == nil && info.Mode().IsRegular():
			return &Project{Root: dir, ConfigPath: configPath, Shadowed: wrongKind, ShadowedWhy: wrongKindWhy}, nil
		case err == nil:
			// It is there; it is the wrong kind of thing. Keep looking -- a
			// real project above must not be hidden by it -- but remember it,
			// so that if nothing is found the answer is what is wrong rather
			// than "dev.yml is missing", which would send the user to create
			// what they are looking at.
			if wrongKind == "" {
				wrongKind, wrongKindWhy = configPath, "is not a regular file"
			}
		case isAbsent(err) && isDanglingLink(configPath):
			// Stat follows the link, so a link to nothing is ENOENT -- the
			// same answer as nothing being there, which would send the user
			// to create a dev.yml their own directory listing shows them.
			if wrongKind == "" {
				wrongKind, wrongKindWhy = configPath, "is a symbolic link whose target is not there"
			}
		case !isAbsent(err):
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

	if wrongKind != "" {
		return nil, fmt.Errorf("%w: %s %s", ErrNotFound, wrongKind, wrongKindWhy)
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
