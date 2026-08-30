// Package incus はIncus instanceのライフサイクル操作を集約する。
//
// CLI層からIncusコマンド文字列を直接組み立てないための境界である
// （仕様 05-incus.md 5.7）。
package incus

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrInstanceNotFound はinstanceが存在しないことを示す。
var ErrInstanceNotFound = errors.New("instance not found")

// Device はIncus deviceの定義（キーと値の素通し）。
type Device map[string]string

// Type はdeviceの型を返す。
func (d Device) Type() string { return d["type"] }

// Instance はIncus instanceの状態。
type Instance struct {
	Name     string            `json:"name"`
	Status   string            `json:"status"`
	Type     string            `json:"type"`
	Profiles []string          `json:"profiles"`
	Config   map[string]string `json:"config"`
	Devices  map[string]Device `json:"devices"`
}

// IsRunning はinstanceが実行中かを返す。
func (i *Instance) IsRunning() bool { return i.Status == "Running" }

// InstanceSpec はinstance作成時の指定。
type InstanceSpec struct {
	Name  string
	Image string
	// Type は container（既定）または virtual-machine。
	Type string
	// Profiles は適用するProfile名。
	Profiles []string
	// NoProfiles が真の場合、Profileを一切適用しない（profiles: [] に対応）。
	NoProfiles bool
	Config     map[string]string
	// Devices は作成時に設定するdevice。
	// profileを適用しない場合、root diskは作成時に必要となる。
	Devices map[string]Device
}

// ExecOptions はコンテナ内でのコマンド実行オプション。
type ExecOptions struct {
	Env  map[string]string
	Cwd  string
	User string
	// TTY が真の場合、擬似端末を割り当てて標準入出力を引き継ぐ。
	TTY    bool
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// WaitOptions はinstanceのready待ちの制御。
type WaitOptions struct {
	Timeout  time.Duration
	Interval time.Duration
}

// Client はIncus操作のインターフェース。テストではfakeへ差し替える。
type Client interface {
	Instance(ctx context.Context, name string) (*Instance, error)
	InstanceExists(ctx context.Context, name string) (bool, error)

	CreateInstance(ctx context.Context, spec InstanceSpec) error
	StartInstance(ctx context.Context, name string) error
	StopInstance(ctx context.Context, name string) error
	DeleteInstance(ctx context.Context, name string) error

	ApplyConfig(ctx context.Context, name string, config map[string]string) error
	UnsetConfig(ctx context.Context, name string, keys []string) error
	ApplyDevices(ctx context.Context, name string, devices map[string]Device) error

	ProfileExists(ctx context.Context, name string) (bool, error)

	Exec(ctx context.Context, name string, argv []string, opt ExecOptions) (int, error)
	WaitReady(ctx context.Context, name string, opt WaitOptions) error
}
