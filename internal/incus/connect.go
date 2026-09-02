package incus

import (
	"context"
	"fmt"

	incusclient "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
	"github.com/lxc/incus/v6/shared/cliconfig"
)

// localRemote is the only Incus operated on.
//
// A remote Incus is out of scope: the workspace is a bind mount of a path on
// this machine, which does not exist on the other end (spec 05-incus.md
// 5.7.1). Naming it explicitly also keeps idev clear of what
// "incus remote switch" left as the default.
const localRemote = "local"

// Target is the Incus to operate on.
type Target struct {
	// Project is the Incus project name. Empty means default.
	Project string
}

// cliConfig is the part of the incus command's configuration idev uses.
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
	return connect(config, target)
}

func connect(config cliConfig, target Target) (*API, error) {
	server, err := config.GetInstanceServer(localRemote)
	if err != nil {
		return nil, fmt.Errorf("connect to the local incus: %w", err)
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
// An alias points at a different image per instance type, and only containers
// are supported, so the container image is what is asked for
// (spec 03-configuration.md 3.4).
func (r *configImageResolver) Resolve(ctx context.Context, ref string) (incusclient.ImageServer, *api.Image, error) {
	type resolved struct {
		server incusclient.ImageServer
		image  *api.Image
		err    error
	}

	done := make(chan resolved, 1)
	go func() {
		server, image, err := r.resolve(ref)
		done <- resolved{server, image, err}
	}()

	select {
	case res := <-done:
		return res.server, res.image, res.err
	case <-ctx.Done():
		// incus's ImageServer takes no context, so the request itself cannot
		// be stopped. Stop waiting for it instead: fetching an image is the
		// slow part of a run and has to stay interruptible.
		return nil, nil, ctx.Err()
	}
}

// Alias splits a reference into the remote's name and the image name, without
// contacting anything.
//
// Creation passes the image name to the daemon rather than the fingerprint
// this package resolved: a simplestreams remote cannot be queried by
// fingerprint, so the daemon can only satisfy one out of its local cache. The
// two views also drift -- the client sees the index as it is now, the daemon
// sees the copy it cached -- and asking it for a fingerprint it has never
// heard of is how 'idev up' broke every time the upstream image was rebuilt.
func (r *configImageResolver) Alias(ref string) (remote, name string, err error) {
	remote, name, err = r.config.ParseRemote(ref)
	if err != nil {
		return "", "", fmt.Errorf("parse image reference %q: %w", ref, err)
	}
	if name == "" {
		return "", "", fmt.Errorf("image reference %q has no image name", ref)
	}
	return remote, name, nil
}

func (r *configImageResolver) resolve(ref string) (incusclient.ImageServer, *api.Image, error) {
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
	if alias, _, err := server.GetImageAliasType(string(api.InstanceTypeContainer), name); err == nil && alias != nil {
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
