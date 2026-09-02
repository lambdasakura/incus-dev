package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSubIDs(t *testing.T) {
	const content = `# comment line

alice:100000:65536
root:1000000:1000000000
malformed line
root:not-a-number:1
`
	ranges := parseSubIDs(strings.NewReader(content))

	if len(ranges) != 2 {
		t.Fatalf("parseSubIDs() = %d ranges, want 2: %+v", len(ranges), ranges)
	}
	if ranges[0].Owner != "alice" || ranges[0].Start != 100000 || ranges[0].Count != 65536 {
		t.Errorf("ranges[0] = %+v", ranges[0])
	}
}

func TestAllowsID(t *testing.T) {
	ranges := []subIDRange{
		{Owner: "alice", Start: 100000, Count: 65536},
		{Owner: "root", Start: 1000000, Count: 1000000000},
	}
	tests := []struct {
		name  string
		owner string
		id    int64
		want  bool
	}{
		{"outside the range", "root", 1000, false},
		{"inside the range", "root", 1000000, true},
		{"the upper bound", "root", 1000000 + 1000000000 - 1, true},
		{"one past the upper bound", "root", 1000000 + 1000000000, false},
		{"a different owner", "alice", 1000, false},
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

	t.Run("permitted", func(t *testing.T) {
		uidPath := write("subuid-ok", "root:1000:1\n")
		gidPath := write("subgid-ok", "root:1000:1\n")

		if err := checkSubIDAllowed(uidPath, gidPath, 1000, 1000); err != nil {
			t.Errorf("checkSubIDAllowed() error = %v, want nil", err)
		}
	})

	t.Run("not permitted", func(t *testing.T) {
		uidPath := write("subuid-ng", "root:1000000:1000000000\n")
		gidPath := write("subgid-ng", "root:1000000:1000000000\n")

		err := checkSubIDAllowed(uidPath, gidPath, 1000, 1000)
		if err == nil {
			t.Fatal("checkSubIDAllowed() = nil error, want error")
		}
		// It says what to do about it.
		for _, want := range []string{"/etc/subuid", "root:1000:1", "idmap: none"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error =\n%v\nwant it to contain %q", err, want)
			}
		}
	})

	// It passes when there is nothing to check by; some environments have no such
	// file.
	t.Run("the file is missing", func(t *testing.T) {
		if err := checkSubIDAllowed(filepath.Join(dir, "nope"), filepath.Join(dir, "nope"), 1000, 1000); err != nil {
			t.Errorf("checkSubIDAllowed() error = %v, want it to pass when it cannot check", err)
		}
	})
}

func TestParseSubIDsSkipsMalformedCount(t *testing.T) {
	ranges := parseSubIDs(strings.NewReader("root:1000:notanumber\nroot:2000:1\n"))

	if len(ranges) != 1 || ranges[0].Start != 2000 {
		t.Errorf("parseSubIDs() = %+v", ranges)
	}
}

func TestCheckSubIDAllowedReportsGIDOnly(t *testing.T) {
	dir := t.TempDir()
	uidPath := filepath.Join(dir, "subuid")
	gidPath := filepath.Join(dir, "subgid")
	if err := os.WriteFile(uidPath, []byte("root:1000:1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gidPath, []byte("root:9999:1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := checkSubIDAllowed(uidPath, gidPath, 1000, 1000)
	if err == nil {
		t.Fatal("error = nil, want a failure when the gid is not permitted")
	}
	if !strings.Contains(err.Error(), "subgid") {
		t.Errorf("error = %v, want it to name subgid", err)
	}
	if strings.Contains(err.Error(), "/etc/subuid: root:1000:1") {
		t.Errorf("error = %v, want it not to list the side that is satisfied", err)
	}
}

// The default check reads the host's /etc/subuid and /etc/subgid. The result
// depends on the host, but a failure has to say what to do about it.
func TestDefaultIDMapCheck(t *testing.T) {
	err := defaultIDMapCheck(os.Getuid(), os.Getgid())
	if err == nil {
		t.Log("raw.idmap is permitted on this host")
		return
	}

	for _, want := range []string{subUIDPath, subGIDPath, "idmap: none"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to contain %q", err, want)
		}
	}
}

// An unreadable gid file leaves nothing to judge by either, so it passes.
func TestCheckSubIDAllowedSkipsWhenGIDFileMissing(t *testing.T) {
	dir := t.TempDir()
	uidPath := filepath.Join(dir, "subuid")
	if err := os.WriteFile(uidPath, []byte("root:1000:1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := checkSubIDAllowed(uidPath, filepath.Join(dir, "missing"), 1000, 1000); err != nil {
		t.Errorf("checkSubIDAllowed() error = %v, want it to pass when it cannot judge", err)
	}
}
