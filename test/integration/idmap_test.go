//go:build integration

package integration_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

const idmapYAML = `
schema: 1
project:
  name: {{PROJECT}}
instance:
  image: {{IMAGE}}
`

// workspace.idmap: auto（既定）は、どちらのホストでもworkspaceを
// 書き込み可能にする。所有者の扱いだけがホスト設定に依存する。
func TestWorkspaceIDMapAuto(t *testing.T) {
	f := newFixture(t, idmapYAML)

	out := f.mustRun("up")

	if !idmapPermitted(t) {
		// rawが使えないホストでは shift へ退避し、その旨を伝えること
		if !strings.Contains(out, "shift") {
			t.Errorf("output = %q, 退避したことを伝えること", out)
		}
		if got := incusOut(t, "config", "get", f.instance, "raw.idmap"); got != "" {
			t.Errorf("raw.idmap = %q, 使えない場合は設定しないこと", got)
		}
	}

	f.mustRun("shell", "--", "sh", "-c", "echo written > /workspace/from-container.txt")

	path := filepath.Join(f.root, "from-container.txt")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("コンテナからホスト側へ書き込めていない: %v", err)
	}

	// rawが使える場合のみ、ホスト側の所有者が実行ユーザーになる
	if idmapPermitted(t) {
		if uid := fileUID(info); uid != os.Getuid() {
			t.Errorf("uid = %d, want %d (ホストの実行ユーザーが所有すること)", uid, os.Getuid())
		}
	}
}

// idmap: shift は追加のホスト設定なしでworkspaceを読み書き可能にする
func TestWorkspaceIDMapShift(t *testing.T) {
	f := newFixture(t, idmapYAML+"workspace:\n  idmap: shift\n")

	f.mustRun("up")

	if got := incusOut(t, "config", "get", f.instance, "raw.idmap"); got != "" {
		t.Errorf("raw.idmap = %q, shiftでは設定しないこと", got)
	}
	if got := f.mustRun("shell", "--", "cat", "/workspace/src/marker.txt"); !strings.Contains(got, "hello from host") {
		t.Errorf("ホストのファイルが読めない: %q", got)
	}
	f.mustRun("shell", "--", "sh", "-c", "echo written > /workspace/shift.txt")

	if _, err := os.Stat(filepath.Join(f.root, "shift.txt")); err != nil {
		t.Errorf("コンテナからホスト側へ書き込めていない: %v", err)
	}
}

// idmap: raw はホストが許可していない場合、instanceを作る前に失敗する
func TestWorkspaceIDMapRawRequiresHostSetup(t *testing.T) {
	if idmapPermitted(t) {
		t.Skip("このホストは raw.idmap を許可しているためスキップします")
	}
	f := newFixture(t, idmapYAML+"workspace:\n  idmap: raw\n")

	out := f.mustFail("up")

	for _, want := range []string{"/etc/subuid", "root:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, %q を含むこと", out, want)
		}
	}
	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "n"); got != "" {
		t.Errorf("検査前にinstanceを作成している: %q", got)
	}
}

// fileUID はファイルの所有uidを返す。
func fileUID(info os.FileInfo) int {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1
	}
	return int(st.Uid)
}

// idmapPermitted は root が現在のuid/gidを対応付けられるかを返す。
func idmapPermitted(t *testing.T) bool {
	t.Helper()

	return subIDAllows(t, "/etc/subuid", os.Getuid()) && subIDAllows(t, "/etc/subgid", os.Getgid())
}

func subIDAllows(t *testing.T, path string, id int) bool {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Split(strings.TrimSpace(line), ":")
		if len(parts) != 3 || parts[0] != "root" {
			continue
		}
		start, err1 := strconv.Atoi(parts[1])
		count, err2 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil {
			continue
		}
		if id >= start && id < start+count {
			return true
		}
	}
	return false
}
