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

// Options は Parse の挙動を制御する。
type Options struct {
	// Root はパス解決の基準となるプロジェクトroot。
	// 空の場合、参照パスの存在検査を行わない。
	Root string
}

// Load は dev.yml を読み込み、validationを行う。
// configPath は <root>/.incus-dev/dev.yml を想定する。
func Load(configPath string) (*Config, error) {
	data, err := os.ReadFile(configPath) //nolint:gosec // 利用者が指定した設定ファイルを読むことが目的
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

// Parse は dev.yml の内容を解釈しvalidationを行う。
func Parse(data []byte, opt Options) (*Config, error) {
	jsonData, err := yaml.YAMLToJSON(data)
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

	// schema versionは他のvalidationより先に確認する（仕様 3.2）。
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

// decodeDocument はJSONを、JSON Schema検証にもそのまま渡せる形へデコードする。
// 数値は json.Number として保持する。
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

// devSchema は同梱されたJSON Schemaを返す。
//
// Schemaはバイナリへ同梱されており、これが壊れているのは利用者の入力ではなく
// ビルド成果物の不具合であるため、regexp.MustCompile と同様にpanicさせる。
// test/structure_test.go と config のテストで常に検証される。
var devSchema = sync.OnceValue(func() *jsonschema.Schema {
	sch, err := compileSchema(schemas.DevV1)
	if err != nil {
		panic("incus-devkit: embedded schema is broken: " + err.Error())
	}
	return sch
})

// compileSchema はJSON SchemaをコンパイルするJSON。
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

// validateSchema はJSON Schemaによる構造検証を行い、問題を ps へ追加する。
func validateSchema(doc any, ps *problems) {
	err := devSchema().Validate(doc)
	if err == nil {
		return
	}

	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		// Validate は *ValidationError しか返さない。
		*ps = append(*ps, Problem{Path: "(root)", Message: err.Error()})
		return
	}

	// 中間ノードは "validation failed" のような要約しか持たないため、
	// 葉ノードだけを収集する。
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

// pointerToPath はJSON Pointerを人間向けの表記へ変換する。
// 例: /provision/0/ansible -> provision[0].ansible
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
