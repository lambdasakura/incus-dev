package incus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	incusclient "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
	"github.com/lxc/incus/v6/shared/cliconfig"
)

// fakeCLIConfig is a fake of the incus command's configuration.
type fakeCLIConfig struct {
	instanceServer *fakeInstanceServer
	imageServer    *fakeImageServer

	instanceErr error
	imageErr    error
	parseErr    error

	// requestedRemote is the remote name GetInstanceServer was given.
	requestedRemote string
	// blockImages, when non-nil, holds GetImageServer until it is closed.
	blockImages chan struct{}
}

func (c *fakeCLIConfig) GetInstanceServer(name string) (incusclient.InstanceServer, error) {
	c.requestedRemote = name
	if c.instanceErr != nil {
		return nil, c.instanceErr
	}
	return c.instanceServer, nil
}

func (c *fakeCLIConfig) GetImageServer(string) (incusclient.ImageServer, error) {
	if c.blockImages != nil {
		<-c.blockImages
	}
	if c.imageErr != nil {
		return nil, c.imageErr
	}
	return c.imageServer, nil
}

func (c *fakeCLIConfig) ParseRemote(raw string) (string, string, error) {
	if c.parseErr != nil {
		return "", "", c.parseErr
	}
	remote, name, ok := strings.Cut(raw, ":")
	if !ok {
		return "", raw, nil
	}
	return remote, name, nil
}

// fakeInstanceServer handles nothing but switching projects.
type fakeInstanceServer struct {
	incusclient.InstanceServer
	project string
}

func (s *fakeInstanceServer) UseProject(name string) incusclient.InstanceServer {
	return &fakeInstanceServer{project: name}
}

// fakeImageServer handles nothing but resolving aliases and images.
type fakeImageServer struct {
	incusclient.ImageServer
	aliases map[string]string
	images  map[string]*api.Image
	err     error
}

// GetImageAliasType returns a different image per instance type, as the real
// simplestreams does.
func (s *fakeImageServer) GetImageAliasType(imageType, name string) (*api.ImageAliasesEntry, string, error) {
	target, ok := s.aliases[imageType+"/"+name]
	if !ok {
		return nil, "", errors.New("alias not found")
	}
	return &api.ImageAliasesEntry{Target: target}, "", nil
}

func (s *fakeImageServer) GetImage(fingerprint string) (*api.Image, string, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	image, ok := s.images[fingerprint]
	if !ok {
		return nil, "", errors.New("image not found")
	}
	return image, "", nil
}

func newFakeCLIConfig() *fakeCLIConfig {
	return &fakeCLIConfig{
		instanceServer: &fakeInstanceServer{},
		imageServer: &fakeImageServer{
			aliases: map[string]string{
				"container/alpine/3.21":       "abc123",
				"virtual-machine/alpine/3.21": "vm456",
			},
			images: map[string]*api.Image{
				"abc123": {Fingerprint: "abc123"},
				"vm456":  {Fingerprint: "vm456"},
			},
		},
	}
}

// serverProject returns the Incus project the connection was set to.
func serverProject(t *testing.T, client *API) string {
	t.Helper()
	server, ok := client.Server.(*fakeInstanceServer)
	if !ok {
		t.Fatalf("Server = %T", client.Server)
	}
	return server.project
}

// Only the local Incus is operated on, whatever "incus remote switch" left as
// the default (spec 05-incus.md 5.7.1).
func TestConnectAlwaysUsesTheLocalRemote(t *testing.T) {
	config := newFakeCLIConfig()

	client, err := connect(config, Target{})
	if err != nil {
		t.Fatalf("connect() error = %v", err)
	}
	if config.requestedRemote != "local" {
		t.Errorf("remote = %q, want the local remote", config.requestedRemote)
	}
	if got := serverProject(t, client); got != "" {
		t.Errorf("project = %q, want no switch when none was asked for", got)
	}
}

func TestConnectUsesTarget(t *testing.T) {
	config := newFakeCLIConfig()

	client, err := connect(config, Target{Project: "dev"})
	if err != nil {
		t.Fatalf("connect() error = %v", err)
	}
	if got := serverProject(t, client); got != "dev" {
		t.Errorf("project = %q, want dev", got)
	}
}

func TestConnectError(t *testing.T) {
	config := newFakeCLIConfig()
	config.instanceErr = errAPI

	_, err := connect(config, Target{})
	if !errors.Is(err, errAPI) || !strings.Contains(err.Error(), "local") {
		t.Errorf("error = %v, want an error that names what it tried to reach", err)
	}
}

// Connect works on the defaults with no configuration, leaving them to
// LoadConfig: it gets past loading and on to the local remote.
//
// What that attempt reports differs by platform — a missing socket on Linux,
// "not a Linux system" elsewhere — so only the stage it reached is checked.
func TestConnectLoadsCLIConfig(t *testing.T) {
	t.Setenv("INCUS_CONF", t.TempDir())
	t.Setenv("INCUS_SOCKET", filepath.Join(t.TempDir(), "does-not-exist.socket"))

	_, err := Connect(Target{})
	if err == nil || !strings.Contains(err.Error(), "connect to the local incus") {
		t.Errorf("error = %v, want it to have reached the local remote", err)
	}
}

// Broken configuration produces an error that says why.
func TestConnectBrokenCLIConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("\tnot: [yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INCUS_CONF", dir)

	_, err := Connect(Target{})
	if err == nil || !strings.Contains(err.Error(), "incus client configuration") {
		t.Errorf("error = %v", err)
	}
}

func TestResolveImage(t *testing.T) {
	config := newFakeCLIConfig()
	r := &configImageResolver{config: config}

	server, image, err := r.Resolve(context.Background(), "images:alpine/3.21")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if server != config.imageServer {
		t.Error("no image source was returned")
	}
	if image.Fingerprint != "abc123" {
		t.Errorf("image = %+v, want the alias resolved to a fingerprint", image)
	}
}

// Without an alias, it is taken for a fingerprint given directly.
func TestResolveImageByFingerprint(t *testing.T) {
	config := newFakeCLIConfig()
	config.imageServer.images["deadbeef"] = &api.Image{Fingerprint: "deadbeef"}
	r := &configImageResolver{config: config}

	_, image, err := r.Resolve(context.Background(), "images:deadbeef")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if image.Fingerprint != "deadbeef" {
		t.Errorf("image = %+v", image)
	}
}

// The same alias points at a different image per instance type, and only the
// container one is ever asked for (spec 03-configuration.md 3.4).
func TestResolveImageAsksForTheContainerImage(t *testing.T) {
	r := &configImageResolver{config: newFakeCLIConfig()}

	_, image, err := r.Resolve(context.Background(), "images:alpine/3.21")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if image.Fingerprint != "abc123" {
		t.Errorf("image = %q, want the container image abc123", image.Fingerprint)
	}
}

// When the alias lookup fails, the error says why that failed too.
func TestResolveImageKeepsAliasError(t *testing.T) {
	config := newFakeCLIConfig()
	config.imageServer.aliases = nil

	_, _, err := (&configImageResolver{config: config}).Resolve(
		context.Background(), "images:alpine/3.21")

	if err == nil || !strings.Contains(err.Error(), "alias lookup") {
		t.Errorf("error = %v, want the alias failure shown as well", err)
	}
}

func TestResolveImageErrors(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		setup   func(*fakeCLIConfig)
		wantMsg string
	}{
		{
			name:    "the reference cannot be parsed",
			ref:     "??",
			setup:   func(c *fakeCLIConfig) { c.parseErr = errAPI },
			wantMsg: "parse image reference",
		},
		{
			name:    "no image name",
			ref:     "images:",
			wantMsg: "no image name",
		},
		{
			name:    "the source cannot be reached",
			ref:     "images:alpine/3.21",
			setup:   func(c *fakeCLIConfig) { c.imageErr = errAPI },
			wantMsg: "connect to image server",
		},
		{
			name:    "the image is not found",
			ref:     "images:alpine/3.21",
			setup:   func(c *fakeCLIConfig) { c.imageServer.err = errAPI },
			wantMsg: "resolve image",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := newFakeCLIConfig()
			if tt.setup != nil {
				tt.setup(config)
			}

			_, _, err := (&configImageResolver{config: config}).Resolve(context.Background(), tt.ref)
			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %v, want %q", err, tt.wantMsg)
			}
		})
	}
}

// *cliconfig.Config satisfies cliConfig.
var _ cliConfig = (*cliconfig.Config)(nil)

// Resolving an image can be interrupted.
//
// Fetching an image is the slow part of `idev up`, and it runs against a
// possibly distant server. Ignoring Ctrl-C there leaves the user with nothing
// to do but kill idev (spec 05-incus.md 5.7.3).
func TestResolveImageIsInterruptible(t *testing.T) {
	config := newFakeCLIConfig()
	config.blockImages = make(chan struct{})
	defer close(config.blockImages)

	ctx, cancel := context.WithCancel(context.Background())
	r := &configImageResolver{config: config}

	done := make(chan error, 1)
	go func() {
		_, _, err := r.Resolve(ctx, "images:alpine/3.21")
		done <- err
	}()

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want the interruption reported", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Resolve() kept waiting after the run was interrupted")
	}
}
