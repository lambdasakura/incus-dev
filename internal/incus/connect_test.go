package incus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	incusclient "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
	"github.com/lxc/incus/v6/shared/cliconfig"
)

// fakeCLIConfig は incus コマンド設定のfake。
type fakeCLIConfig struct {
	instanceServer *fakeInstanceServer
	imageServer    *fakeImageServer

	instanceErr error
	imageErr    error
	parseErr    error

	// requestedRemote は GetInstanceServer に渡されたremote名。
	requestedRemote string
}

func (c *fakeCLIConfig) GetInstanceServer(name string) (incusclient.InstanceServer, error) {
	c.requestedRemote = name
	if c.instanceErr != nil {
		return nil, c.instanceErr
	}
	return c.instanceServer, nil
}

func (c *fakeCLIConfig) GetImageServer(string) (incusclient.ImageServer, error) {
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

// fakeInstanceServer は project の切り替えだけを扱う。
type fakeInstanceServer struct {
	incusclient.InstanceServer
	project string
}

func (s *fakeInstanceServer) UseProject(name string) incusclient.InstanceServer {
	return &fakeInstanceServer{project: name}
}

// fakeImageServer は alias と image の解決だけを扱う。
type fakeImageServer struct {
	incusclient.ImageServer
	aliases map[string]string
	images  map[string]*api.Image
	err     error
}

// GetImageAliasType はinstance種別ごとに別のimageを返す。
// 本物のsimplestreamsも種別でimageを分けている。
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

// serverProject は接続先に設定されたIncus projectを返す。
func serverProject(t *testing.T, client *API) string {
	t.Helper()
	server, ok := client.Server.(*fakeInstanceServer)
	if !ok {
		t.Fatalf("Server = %T", client.Server)
	}
	return server.project
}

func TestConnectUsesDefaultRemote(t *testing.T) {
	config := newFakeCLIConfig()

	client, err := connect(config, "local", Target{})
	if err != nil {
		t.Fatalf("connect() error = %v", err)
	}
	if config.requestedRemote != "local" {
		t.Errorf("remote = %q, 既定のremoteを使うこと", config.requestedRemote)
	}
	if got := serverProject(t, client); got != "" {
		t.Errorf("project = %q, 指定が無ければ切り替えないこと", got)
	}
}

func TestConnectUsesTarget(t *testing.T) {
	config := newFakeCLIConfig()

	client, err := connect(config, "local", Target{Remote: "lab", Project: "dev"})
	if err != nil {
		t.Fatalf("connect() error = %v", err)
	}
	if config.requestedRemote != "lab" {
		t.Errorf("remote = %q", config.requestedRemote)
	}
	if got := serverProject(t, client); got != "dev" {
		t.Errorf("project = %q, want dev", got)
	}
}

func TestConnectError(t *testing.T) {
	config := newFakeCLIConfig()
	config.instanceErr = errAPI

	_, err := connect(config, "local", Target{Remote: "lab"})
	if !errors.Is(err, errAPI) || !strings.Contains(err.Error(), "lab") {
		t.Errorf("error = %v, 接続先が分かるエラーにすること", err)
	}
}

// 設定が無くてもConnectは既定値で動く（LoadConfigの既定に委ねる）
func TestConnectLoadsCLIConfig(t *testing.T) {
	t.Setenv("INCUS_CONF", t.TempDir())

	if _, err := Connect(Target{Remote: "does-not-exist"}); err == nil {
		t.Error("未知のremoteはエラーになること")
	}
}

// 設定が壊れている場合は、原因が分かるエラーにする
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

	server, image, err := r.Resolve(context.Background(), "images:alpine/3.21", "container")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if server != config.imageServer {
		t.Error("image取得元が返されていない")
	}
	if image.Fingerprint != "abc123" {
		t.Errorf("image = %+v, aliasをfingerprintへ解決すること", image)
	}
}

// alias が無い場合は fingerprint 直接指定とみなす
func TestResolveImageByFingerprint(t *testing.T) {
	config := newFakeCLIConfig()
	config.imageServer.images["deadbeef"] = &api.Image{Fingerprint: "deadbeef"}
	r := &configImageResolver{config: config}

	_, image, err := r.Resolve(context.Background(), "images:deadbeef", "container")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if image.Fingerprint != "deadbeef" {
		t.Errorf("image = %+v", image)
	}
}

// 同じaliasでも、instance種別によって別のimageを指す
func TestResolveImageByInstanceType(t *testing.T) {
	tests := []struct {
		instanceType string
		want         string
	}{
		{"container", "abc123"},
		{"virtual-machine", "vm456"},
		{"", "abc123"}, // 未指定は container
	}

	for _, tt := range tests {
		t.Run(tt.instanceType, func(t *testing.T) {
			r := &configImageResolver{config: newFakeCLIConfig()}

			_, image, err := r.Resolve(context.Background(), "images:alpine/3.21", tt.instanceType)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if image.Fingerprint != tt.want {
				t.Errorf("image = %q, want %q", image.Fingerprint, tt.want)
			}
		})
	}
}

// aliasを引けなかった場合、その理由もエラーへ含める
func TestResolveImageKeepsAliasError(t *testing.T) {
	config := newFakeCLIConfig()
	config.imageServer.aliases = nil

	_, _, err := (&configImageResolver{config: config}).Resolve(
		context.Background(), "images:alpine/3.21", "container")

	if err == nil || !strings.Contains(err.Error(), "alias lookup") {
		t.Errorf("error = %v, aliasの失敗も示すこと", err)
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
			name:    "参照を解釈できない",
			ref:     "??",
			setup:   func(c *fakeCLIConfig) { c.parseErr = errAPI },
			wantMsg: "parse image reference",
		},
		{
			name:    "image名が無い",
			ref:     "images:",
			wantMsg: "no image name",
		},
		{
			name:    "取得元へ接続できない",
			ref:     "images:alpine/3.21",
			setup:   func(c *fakeCLIConfig) { c.imageErr = errAPI },
			wantMsg: "connect to image server",
		},
		{
			name:    "imageが見つからない",
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

			_, _, err := (&configImageResolver{config: config}).Resolve(context.Background(), tt.ref, "container")
			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %v, want %q", err, tt.wantMsg)
			}
		})
	}
}

// *cliconfig.Config を cliConfig として扱えること
var _ cliConfig = (*cliconfig.Config)(nil)
