package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"sigs.k8s.io/yaml"

	"github.com/lambdasakura/incus-dev/schemas"
)

// Options controls how Parse behaves.
type Options struct {
	// Root is the project root that paths are resolved from. Left empty,
	// referenced paths are not checked for existence.
	Root string
}

// Load reads dev.yml and validates it. configPath is expected to be
// <root>/.incus-dev/dev.yml.
func Load(configPath string) (*Config, error) {
	data, err := os.ReadFile(configPath) //nolint:gosec // reading the configuration file the user named is the point
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", configPath, err)
	}
	root := filepath.Dir(filepath.Dir(configPath))
	c, err := Parse(data, Options{Root: root})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}
	return c, nil
}

// Parse interprets and validates the contents of dev.yml.
func Parse(data []byte, opt Options) (*Config, error) {
	// Strict, so a key written twice is an error. YAML would let the last one
	// win, and a duplicated section would then be discarded without a word.
	jsonData, err := yaml.YAMLToJSONStrict(data)
	if err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	doc, err := decodeDocument(jsonData)
	if err != nil {
		return nil, err
	}
	raw, ok := doc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("parse yaml: document must be a mapping")
	}

	// The schema version is checked before anything else (spec 3.2).
	if err := checkSchemaVersion(raw); err != nil {
		return nil, err
	}

	var ps problems
	validateSchema(doc, &ps)
	if len(ps) > 0 {
		return nil, ps.err()
	}

	var c Config
	dec := json.NewDecoder(bytes.NewReader(jsonData))
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("decode configuration: %w", err)
	}
	c.Root = opt.Root

	validateSemantics(&c, raw, &ps)
	if len(ps) > 0 {
		return nil, ps.err()
	}
	return &c, nil
}

// decodeDocument decodes JSON into a shape that can be handed to JSON Schema
// validation as it is, keeping numbers as json.Number.
func decodeDocument(jsonData []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(jsonData))
	dec.UseNumber()

	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	return doc, nil
}

func checkSchemaVersion(raw map[string]any) error {
	v, ok := raw["schema"]
	if !ok {
		return &ValidationError{Problems: []Problem{{
			Path:    "schema",
			Message: fmt.Sprintf("required field is missing (supported: %d)", SchemaVersion),
		}}}
	}
	n, ok := v.(json.Number)
	if !ok {
		return &ValidationError{Problems: []Problem{{
			Path:    "schema",
			Message: fmt.Sprintf("must be the integer %d", SchemaVersion),
		}}}
	}
	got, err := n.Int64()
	if err != nil || got != SchemaVersion {
		return &ValidationError{Problems: []Problem{{
			Path:    "schema",
			Message: fmt.Sprintf("unsupported schema version %s (supported: %d)", n.String(), SchemaVersion),
		}}}
	}
	return nil
}

// devSchema returns the embedded JSON Schema.
//
// The schema is embedded in the binary, so a broken one is a defect in the
// build artifact rather than in the user's input; panic on it, as
// regexp.MustCompile does. test/structure_test.go and the config tests check
// it on every run.
var devSchema = sync.OnceValue(func() *jsonschema.Schema {
	sch, err := compileSchema(schemas.DevV1)
	if err != nil {
		panic("incus-dev: embedded schema is broken: " + err.Error())
	}
	return sch
})

// compileSchema compiles a JSON Schema.
func compileSchema(raw []byte) (*jsonschema.Schema, error) {
	const url = "https://github.com/lambdasakura/incus-dev/schemas/dev-v1.schema.json"

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("add schema: %w", err)
	}
	return c.Compile(url)
}

// validateSchema checks the structure against the JSON Schema and appends what
// it finds to ps.
func validateSchema(doc any, ps *problems) {
	err := devSchema().Validate(doc)
	if err == nil {
		return
	}

	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		// Validate returns nothing but a *ValidationError.
		*ps = append(*ps, Problem{Path: "(root)", Message: err.Error()})
		return
	}

	// Intermediate nodes carry only a summary such as "validation failed", so
	// collect the leaves alone.
	found := make([]Problem, 0, 8)
	collectLeafProblems(verr, message.NewPrinter(language.English), &found)
	sort.SliceStable(found, func(i, j int) bool { return found[i].Path < found[j].Path })
	*ps = append(*ps, found...)
}

func collectLeafProblems(e *jsonschema.ValidationError, p *message.Printer, out *[]Problem) {
	if len(e.Causes) == 0 {
		*out = append(*out, Problem{
			Path:    pointerToPath("/" + strings.Join(e.InstanceLocation, "/")),
			Message: e.ErrorKind.LocalizedString(p),
		})
		return
	}
	for _, cause := range e.Causes {
		collectLeafProblems(cause, p, out)
	}
}

// pointerToPath rewrites a JSON Pointer into something a person can read.
// For example /provision/0/ansible becomes provision[0].ansible.
func pointerToPath(ptr string) string {
	ptr = strings.TrimPrefix(ptr, "/")
	if ptr == "" {
		return "(root)"
	}
	var sb strings.Builder
	for _, tok := range strings.Split(ptr, "/") {
		tok = strings.ReplaceAll(tok, "~1", "/")
		tok = strings.ReplaceAll(tok, "~0", "~")
		if _, err := strconv.Atoi(tok); err == nil {
			fmt.Fprintf(&sb, "[%s]", tok)
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte('.')
		}
		sb.WriteString(tok)
	}
	return sb.String()
}
