package test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The Client interface is the one spec block that claims to list exactly what
// the implementation has: spec 05-incus.md 5.7 says it holds only the
// operations actually used. A listing nobody checks stops being that within a
// release or two, so this checks it.
func TestSpecListsTheRealClientInterface(t *testing.T) {
	spec := readSpecInterface(t, "../docs/spec/05-incus.md", "Client")
	declared := readGoInterface(t, "../internal/incus/client.go", "Client")

	for _, m := range declared {
		if !slices.Contains(spec, m) {
			t.Errorf("Client.%s is missing from spec 05-incus.md 5.7", m)
		}
	}
	for _, m := range spec {
		if !slices.Contains(declared, m) {
			t.Errorf("spec 05-incus.md 5.7 lists Client.%s, which no longer exists", m)
		}
	}
}

// specMethod matches a method line in the spec's Go block: a name followed by
// its parameter list.
var specMethod = regexp.MustCompile(`^\s*([A-Z][A-Za-z0-9]*)\(`)

// readSpecInterface pulls the method names out of the fenced Go block that
// declares the named interface.
func readSpecInterface(t *testing.T, path, name string) []string {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(string(body), "\n")
	start := slices.Index(lines, "type "+name+" interface {")
	if start < 0 {
		t.Fatalf("%s: no %q interface block", path, name)
	}

	var out []string
	for _, line := range lines[start+1:] {
		if strings.HasPrefix(line, "}") {
			return out
		}
		if m := specMethod.FindStringSubmatch(line); m != nil {
			out = append(out, m[1])
		}
	}
	t.Fatalf("%s: the %q block is not closed", path, name)
	return nil
}

// readGoInterface returns the method names the interface declares.
func readGoInterface(t *testing.T, path, name string) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || spec.Name.Name != name {
			return true
		}
		iface, ok := spec.Type.(*ast.InterfaceType)
		if !ok {
			return true
		}
		for _, m := range iface.Methods.List {
			for _, id := range m.Names {
				out = append(out, id.Name)
			}
		}
		return false
	})
	if len(out) == 0 {
		t.Fatalf("%s: no methods found on %q", path, name)
	}
	return out
}
