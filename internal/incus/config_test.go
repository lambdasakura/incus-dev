package incus

import "testing"

func TestSameIDMapping(t *testing.T) {
	tests := []struct {
		name       string
		want, have string
		same       bool
	}{
		{"the same text", "both 1000 0", "both 1000 0", true},
		{"both expands to uid and gid", "uid 1000 0\ngid 1000 0", "both 1000 0", true},
		{"order does not matter", "gid 1000 0\nuid 1000 0", "both 1000 0", true},
		// A value written by anything but idev may space its fields
		// differently; that is not a change worth a restart.
		{"whitespace inside a line does not matter", "uid  1000   0", "uid 1000 0", true},
		{"a trailing newline does not matter", "both 1000 0\n", "both 1000 0", true},
		// A line idev does not write is compared trimmed, so indentation in
		// a hand-edited value is not a difference.
		{"space around a line idev did not write", "  keep me  ", "keep me", true},
		{"a different id is a different mapping", "both 1001 0", "both 1000 0", false},
		// A "both" line with anything after it is not idev's shape, so it is
		// compared whole rather than expanded from its first three fields.
		{"a longer both line is not truncated", "both 1000 0 junk", "both 1000 0", false},
		{"an extra line is a difference", "uid 1000 0\ngid 1000 0", "uid 1000 0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SameIDMapping(tt.want, tt.have); got != tt.same {
				t.Errorf("SameIDMapping(%q, %q) = %v, want %v", tt.want, tt.have, got, tt.same)
			}
		})
	}
}
