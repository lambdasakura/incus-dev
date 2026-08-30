package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSubIDs(t *testing.T) {
	const content = `# comment line

sakura:100000:65536
root:1000000:1000000000
malformed line
root:not-a-number:1
`
	ranges := parseSubIDs(strings.NewReader(content))

	if len(ranges) != 2 {
		t.Fatalf("parseSubIDs() = %d ranges, want 2: %+v", len(ranges), ranges)
	}
	if ranges[0].Owner != "sakura" || ranges[0].Start != 100000 || ranges[0].Count != 65536 {
		t.Errorf("ranges[0] = %+v", ranges[0])
	}
}

func TestAllowsID(t *testing.T) {
	ranges := []subIDRange{
		{Owner: "sakura", Start: 100000, Count: 65536},
		{Owner: "root", Start: 1000000, Count: 1000000000},
	}
	tests := []struct {
		name  string
		owner string
		id    int64
		want  bool
	}{
		{"範囲外", "root", 1000, false},
		{"範囲内", "root", 1000000, true},
		{"上端", "root", 1000000 + 1000000000 - 1, true},
		{"上端の次", "root", 1000000 + 1000000000, false},
		{"別の所有者", "sakura", 1000, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := allowsID(ranges, tt.owner, tt.id); got != tt.want {
				t.Errorf("allowsID(%q, %d) = %v, want %v", tt.owner, tt.id, got, tt.want)
			}
		})
	}
}

func TestCheckSubIDAllowed(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("許可されている", func(t *testing.T) {
		uidPath := write("subuid-ok", "root:1000:1\n")
		gidPath := write("subgid-ok", "root:1000:1\n")

		if err := checkSubIDAllowed(uidPath, gidPath, 1000, 1000); err != nil {
			t.Errorf("checkSubIDAllowed() error = %v, want nil", err)
		}
	})

	t.Run("許可されていない", func(t *testing.T) {
		uidPath := write("subuid-ng", "root:1000000:1000000000\n")
		gidPath := write("subgid-ng", "root:1000000:1000000000\n")

		err := checkSubIDAllowed(uidPath, gidPath, 1000, 1000)
		if err == nil {
			t.Fatal("checkSubIDAllowed() = nil error, want error")
		}
		// 対処方法を示すこと
		for _, want := range []string{"/etc/subuid", "root:1000:1", "idmap: none"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error =\n%v\n%q を含むこと", err, want)
			}
		}
	})

	// 検査できない場合は通す（環境によってはファイルが無い）
	t.Run("ファイルが無い", func(t *testing.T) {
		if err := checkSubIDAllowed(filepath.Join(dir, "nope"), filepath.Join(dir, "nope"), 1000, 1000); err != nil {
			t.Errorf("checkSubIDAllowed() error = %v, 検査不能なら通すこと", err)
		}
	})
}
