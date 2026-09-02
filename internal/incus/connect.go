package incus

import (
	"context"
	"fmt"

	incusclient "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
	"github.com/lxc/incus/v6/shared/cliconfig"
)

// Target is the Incus to operate on.
type Target struct {
	// Remote is the remote name. Only when it is empty is the incus default
	// remote used. The CLI default is "local", and it is unaffected by
	// incus remote switch.
	Remote string
	// Project is the Incus project name. Empty means default.
	Project string
}

// cliConfig is the part of the incus command's configuration devkit uses.
type cliConfig interface {
	// GetInstanceServer connects to a remote's Incus API.
	GetInstanceServer(name string) (incusclient.InstanceServer, error)
	// GetImageServer connects to a remote's image server.
	GetImageServer(name string) (incusclient.ImageServer, error)
	// ParseRemote splits a reference such as images:alpine/3.21 into a remote
	// and a name.
	ParseRemote(raw string) (string, string, error)
}

// Connect returns a Client connected to the Incus API.
//
// It resolves remotes and image servers with the same configuration as the
// incus command (~/.config/incus/config.yml), so that the two behave alike.
// An empty path lets LoadConfig find it, falling back to the defaults when
// there is no configuration at all.
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

// configImageResolver resolves image references with the same configuration as
// the incus command.
type configImageResolver struct {
	config cliConfig
}

// Resolve turns a reference such as images:ubuntu/24.04 into a source and an
// image.
//
// instanceType is used to resolve the alias: the same alias — images:debian/12,
// say — points at a different image for a container than for a virtual
// machine.
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

	// Resolve the alias (ubuntu/24.04 and the like) to a fingerprint. When it
	// does not resolve, take it for a fingerprint given directly.
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
			// The alias lookup failing may be the cause, so show both.
			return nil, nil, fmt.Errorf("resolve image %q: %w (alias lookup failed: %w)", ref, err, aliasErr)
		}
		return nil, nil, fmt.Errorf("resolve image %q: %w", ref, err)
	}
	return server, image, nil
}

// instanceTypeOrDefault treats an empty type as container.
func instanceTypeOrDefault(instanceType string) string {
	if instanceType == "" {
		return "container"
	}
	return instanceType
}
