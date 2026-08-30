// Package project はプロジェクトroot（.incus-dev/dev.yml を持つディレクトリ）の
// 探索を担当する。
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// ConfigDir はプロジェクト側の設定ディレクトリ名。
	ConfigDir = ".incus-dev"
	// ConfigFile は開発環境定義ファイル名。
	ConfigFile = "dev.yml"
)

// ErrNotFound はプロジェクトrootが見つからなかったことを示す。
var ErrNotFound = errors.New("project root not found")

// Project は探索されたプロジェクトを表す。
type Project struct {
	// Root はプロジェクトrootの絶対パス。
	Root string
	// ConfigPath は dev.yml の絶対パス。
	ConfigPath string
}

// ConfigDirPath は .incus-dev ディレクトリの絶対パスを返す。
func (p *Project) ConfigDirPath() string {
	return filepath.Join(p.Root, ConfigDir)
}

// Discover は startDir から親方向へ .incus-dev/dev.yml を探索する。
// 最も近いプロジェクトrootを返す。
func Discover(startDir string) (*Project, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return nil, fmt.Errorf("resolve start directory %q: %w", startDir, err)
	}
	// t.TempDir() などがsymlinkを含む場合に備えて正規化する。
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}

	// .incus-dev/ はあるが dev.yml が無いディレクトリ（設定漏れの可能性）。
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
