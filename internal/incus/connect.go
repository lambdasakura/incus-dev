package incus

import (
	"context"
	"fmt"

	incusclient "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
	"github.com/lxc/incus/v6/shared/cliconfig"
)

// Target は操作対象のIncus。
type Target struct {
	// Remote はremote名。空またはlocalならローカルのIncus。
	Remote string
	// Project はIncus project名。空なら default。
	Project string
}

// cliConfig は incus コマンドの設定のうち、devkitが使う部分。
type cliConfig interface {
	// GetInstanceServer はremoteのIncus APIへ接続する。
	GetInstanceServer(name string) (incusclient.InstanceServer, error)
	// GetImageServer はremoteのimage配布サーバへ接続する。
	GetImageServer(name string) (incusclient.ImageServer, error)
	// ParseRemote は images:alpine/3.21 のような参照をremoteと名前に分ける。
	ParseRemote(raw string) (string, string, error)
}

// Connect は Incus API へ接続した Client を返す。
//
// remoteやimageサーバの解決には incus コマンドと同じ設定
// （~/.config/incus/config.yml）を使う。挙動を揃えるためである。
// パスを空にすると LoadConfig が自動判別し、設定が無ければ既定を返す。
func Connect(target Target, terminal Client) (*API, error) {
	config, err := cliconfig.LoadConfig("")
	if err != nil {
		return nil, fmt.Errorf("load incus client configuration: %w", err)
	}
	return connect(config, config.DefaultRemote, target, terminal)
}

func connect(config cliConfig, defaultRemote string, target Target, terminal Client) (*API, error) {
	remote := target.Remote
	if remote == "" {
		remote = defaultRemote
	}

	server, err := config.GetInstanceServer(remote)
	if err != nil {
		return nil, fmt.Errorf("connect to incus remote %q: %w", remote, err)
	}
	if target.Project != "" {
		server = server.UseProject(target.Project)
	}

	return &API{
		Server:   server,
		Images:   &configImageResolver{config: config},
		Terminal: terminal,
	}, nil
}

// configImageResolver は incus コマンドと同じ設定でimage参照を解決する。
type configImageResolver struct {
	config cliConfig
}

// Resolve は images:ubuntu/24.04 のような参照から、取得元とimageを返す。
func (r *configImageResolver) Resolve(_ context.Context, ref string) (incusclient.ImageServer, *api.Image, error) {
	remote, name, err := r.config.ParseRemote(ref)
	if err != nil {
		return nil, nil, fmt.Errorf("parse image reference %q: %w", ref, err)
	}
	if name == "" {
		return nil, nil, fmt.Errorf("image reference %q has no image name", ref)
	}

	server, err := r.config.GetImageServer(remote)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to image server %q: %w", remote, err)
	}

	// エイリアス（ubuntu/24.04 など）を fingerprint へ解決する。
	fingerprint := name
	if alias, _, err := server.GetImageAlias(name); err == nil && alias != nil {
		fingerprint = alias.Target
	}

	image, _, err := server.GetImage(fingerprint)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve image %q: %w", ref, err)
	}
	return server, image, nil
}
