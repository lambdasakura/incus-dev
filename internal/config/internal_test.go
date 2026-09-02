package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScalarString(t *testing.T) {
	tests := []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{`"text"`, "text", false},
		{`8`, "8", false},
		{`1.5`, "1.5", false},
		{`true`, "true", false},
		{`false`, "false", false},
		{`{"a":1}`, "", true},
		{`[1]`, "", true},
		{`null`, "", true},
		{`}`, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := scalarString(json.RawMessage(tt.raw))

			if tt.wantErr {
				if err == nil {
					t.Fatalf("scalarString(%s) = %q, want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("scalarString(%s) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("scalarString(%s) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestStringMapUnmarshalErrors(t *testing.T) {
	var m StringMap

	if err := m.UnmarshalJSON([]byte(`["not an object"]`)); err == nil {
		t.Error("want an array rejected")
	}
	if err := m.UnmarshalJSON([]byte(`{"key": {"nested": 1}}`)); err == nil {
		t.Error("want a non-scalar value rejected")
	} else if !strings.Contains(err.Error(), "key") {
		t.Errorf("error = %q, want it to name the key", err.Error())
	}
}

func TestStepUnmarshalError(t *testing.T) {
	var s Step

	if err := s.UnmarshalJSON([]byte(`"not an object"`)); err == nil {
		t.Error("want a string rejected")
	}
}

// Both set, and neither set, are not decode errors; validation reports them.
func TestStepUnmarshalLeavesAmbiguousStepEmpty(t *testing.T) {
	tests := []string{
		`{"run": "cmd", "ansible": {"playbook": "p.yml"}}`,
		`{"name": "empty"}`,
	}

	for _, in := range tests {
		var s Step
		if err := s.UnmarshalJSON([]byte(in)); err != nil {
			t.Fatalf("UnmarshalJSON(%s) error = %v", in, err)
		}
		if s.Run != nil || s.Ansible != nil {
			t.Errorf("UnmarshalJSON(%s) = %+v, want neither field set", in, s)
		}
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in           string
		major, minor int
		wantErr      bool
	}{
		{"1", 1, 0, false},
		{"1.2", 1, 2, false},
		{"1.2.3", 1, 2, false},
		{"1.2.3.4", 0, 0, true},
		{"x", 0, 0, true},
		{"1.x", 0, 0, true},
		{"1.2.x", 0, 0, true},
		{"", 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			major, minor, err := parseVersion(tt.in)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseVersion(%q) = %d, %d, want error", tt.in, major, minor)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVersion(%q) error = %v", tt.in, err)
			}
			if major != tt.major || minor != tt.minor {
				t.Errorf("parseVersion(%q) = %d, %d, want %d, %d", tt.in, major, minor, tt.major, tt.minor)
			}
		})
	}
}

func TestRuntimeCompatibleReportsInvalidCurrent(t *testing.T) {
	if _, _, err := runtimeCompatible("1.0", "broken"); err == nil {
		t.Error("want an error when the current version is malformed")
	}
}

// The JSON Schema rejects steps that are not a list, so validateSteps does
// nothing with them.
func TestValidateStepsIgnoresNonList(t *testing.T) {
	var ps problems
	validateSteps(map[string]any{"provision": "not a list"}, "provision", true, &ps)
	validateSteps(map[string]any{"provision": []any{"not a map"}}, "provision", true, &ps)

	if len(ps) != 0 {
		t.Errorf("problems = %v, want none", ps)
	}
}

func TestCompileSchemaRejectsBrokenInput(t *testing.T) {
	if _, err := compileSchema([]byte("{")); err == nil {
		t.Error("want broken JSON rejected")
	}
	if _, err := compileSchema([]byte(`{"type": 123}`)); err == nil {
		t.Error("want an invalid schema rejected")
	}
}

// The embedded schema always compiles.
func TestEmbeddedSchemaCompiles(t *testing.T) {
	if devSchema() == nil {
		t.Error("devSchema() = nil")
	}
}

func TestPointerToPath(t *testing.T) {
	tests := map[string]string{
		"":                           "(root)",
		"/":                          "(root)",
		"/instance/image":            "instance.image",
		"/provision/0/ansible":       "provision[0].ansible",
		"/instance/config/limits~1a": "instance.config.limits/a",
	}

	for in, want := range tests {
		if got := pointerToPath(in); got != want {
			t.Errorf("pointerToPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeDocumentRejectsInvalidJSON(t *testing.T) {
	if _, err := decodeDocument([]byte("{")); err == nil {
		t.Error("decodeDocument() = nil error, want error")
	}
}

func TestCompileSchemaRejectsNonObject(t *testing.T) {
	if _, err := compileSchema([]byte(`[1, 2]`)); err == nil {
		t.Error("want an array rejected as a schema")
	}
}

func TestCompileSchemaRejectsInvalidID(t *testing.T) {
	if _, err := compileSchema([]byte(`{"$id": "::::"}`)); err == nil {
		t.Error("want an invalid $id rejected")
	}
}
