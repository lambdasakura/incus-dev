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
	// Remote はremote名。空の場合のみ incus の既定remoteを使う。
	// CLIの既定は "local" であり、incus remote switch の影響を受けない。
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
func Connect(target Target) (*API, error) {
	config, err := cliconfig.LoadConfig("")
	if err != nil {
		return nil, fmt.Errorf("load incus client configuration: %w", err)
	}
	return connect(config, config.DefaultRemote, target)
}

func connect(config cliConfig, defaultRemote string, target Target) (*API, error) {
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
		Server: server,
		Images: &configImageResolver{config: config},
	}, nil
}

// configImageResolver は incus コマンドと同じ設定でimage参照を解決する。
type configImageResolver struct {
	config cliConfig
}

// Resolve は images:ubuntu/24.04 のような参照から、取得元とimageを返す。
//
// instanceType はaliasの解決に使う。同じalias（例 images:debian/12）が
// container用とvirtual-machine用で別のimageを指すためである。
func (r *configImageResolver) Resolve(_ context.Context, ref, instanceType string) (incusclient.ImageServer, *api.Image, error) {
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
	// 解決できない場合は fingerprint 直指定とみなす。
	fingerprint := name
	aliasErr := error(nil)
	if alias, _, err := server.GetImageAliasType(instanceTypeOrDefault(instanceType), name); err == nil && alias != nil {
		fingerprint = alias.Target
	} else {
		aliasErr = err
	}

	image, _, err := server.GetImage(fingerprint)
	if err != nil {
		if aliasErr != nil {
			// aliasを引けなかったことが原因のこともあるため、両方示す。
			return nil, nil, fmt.Errorf("resolve image %q: %w (alias lookup failed: %w)", ref, err, aliasErr)
		}
		return nil, nil, fmt.Errorf("resolve image %q: %w", ref, err)
	}
	return server, image, nil
}

// instanceTypeOrDefault は空の指定を container として扱う。
func instanceTypeOrDefault(instanceType string) string {
	if instanceType == "" {
		return "container"
	}
	return instanceType
}
