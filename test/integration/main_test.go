//go:build integration

// Package integration はIncus実機に対する統合テストを提供する。
//
//	go test -tags integration ./test/integration/...
package integration_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	// idevBin はテスト対象のバイナリパス。
	idevBin string
	// testImage は軽量な検証用イメージ。
	testImage = envOr("IDEV_TEST_IMAGE", "images:alpine/3.21")
	// ansibleImage は python3 を含むイメージ（ansibleステップの検証用）。
	ansibleImage = envOr("IDEV_TEST_ANSIBLE_IMAGE", "images:ubuntu/noble")
	// bootstrapImage は python3 を含まないDebian系イメージ（既定bootstrapの検証用）。
	bootstrapImage = envOr("IDEV_TEST_BOOTSTRAP_IMAGE", "images:debian/12")
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// skipReason はIncusが利用できない理由。空なら利用可能。
var skipReason string

// requireIncus はIncusが利用できない場合にテストをスキップする。
// 実行されなかったことが結果から分かるよう、passではなくskipにする。
func requireIncus(t *testing.T) {
	t.Helper()

	if skipReason != "" {
		t.Skip(skipReason)
	}
}

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("incus"); err != nil {
		skipReason = "incus コマンドが見つかりません"
	} else if out, err := exec.Command("incus", "info").CombinedOutput(); err != nil {
		skipReason = fmt.Sprintf("Incus daemonへ接続できません: %v\n%s", err, out)
	}
	if skipReason != "" {
		os.Exit(m.Run())
	}

	dir, err := os.MkdirTemp("", "idev-integration-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	idevBin = filepath.Join(dir, "idev")
	build := exec.Command("go", "build", "-o", idevBin, "../../cmd/idev")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "ビルドに失敗しました: %v\n%s", err, out)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// fixture はテスト用のプロジェクトディレクトリ。
type fixture struct {
	t        *testing.T
	root     string
	project  string
	instance string
}

// newFixture は一意なプロジェクト名を持つ .incus-dev/dev.yml を作る。
// devYAML 中の {{IMAGE}} と {{PROJECT}} は置換される。
func newFixture(t *testing.T, devYAML string) *fixture {
	t.Helper()

	requireIncus(t)

	root := t.TempDir()
	project := fmt.Sprintf("idev-it-%d", time.Now().UnixNano()%1e9)
	instance := "dev-" + project

	body := strings.ReplaceAll(devYAML, "{{PROJECT}}", project)
	body = strings.ReplaceAll(body, "{{IMAGE}}", testImage)
	body = strings.ReplaceAll(body, "{{ANSIBLE_IMAGE}}", ansibleImage)

	writeFile(t, filepath.Join(root, ".incus-dev", "dev.yml"), body)
	writeFile(t, filepath.Join(root, "src", "marker.txt"), "hello from host\n")

	f := &fixture{t: t, root: root, project: project, instance: instance}
	t.Cleanup(f.cleanup)
	return f
}

func (f *fixture) cleanup() {
	_ = exec.Command("incus", "delete", "--force", f.instance).Run()
}

// run は idev を実行し、結合された出力を返す。
func (f *fixture) run(args ...string) (string, error) {
	f.t.Helper()

	cmd := exec.Command(idevBin, args...)
	cmd.Dir = f.root
	cmd.Env = os.Environ() // t.Setenv で設定した値を引き継ぐ

	out, err := cmd.CombinedOutput()
	return string(out), err
}

// mustRun は idev の実行が成功することを要求する。
func (f *fixture) mustRun(args ...string) string {
	f.t.Helper()

	out, err := f.run(args...)
	if err != nil {
		f.t.Fatalf("idev %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// mustFail は idev の実行が失敗することを要求する。
func (f *fixture) mustFail(args ...string) string {
	f.t.Helper()

	out, err := f.run(args...)
	if err == nil {
		f.t.Fatalf("idev %s succeeded unexpectedly:\n%s", strings.Join(args, " "), out)
	}
	return out
}

// incusOut は incus コマンドの標準出力を返す。
func incusOut(t *testing.T, args ...string) string {
	t.Helper()

	out, err := exec.Command("incus", args...).Output()
	if err != nil {
		t.Fatalf("incus %s failed: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func requireCommand(t *testing.T, name string) {
	t.Helper()

	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s が見つからないためスキップします", name)
	}
}

// runIncus は incus コマンドを実行し、出力とエラーを返す。
func runIncus(args ...string) (string, error) {
	out, err := exec.Command("incus", args...).CombinedOutput()
	return string(out), err
}
