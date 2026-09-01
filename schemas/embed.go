// Package schemas embeds the JSON Schema for dev.yml into the binary.
//
// This schema is the only asset devkit embeds (spec 02-repository-layout.md 2.4).
package schemas

import _ "embed"

// DevV1 is the JSON Schema for schema: 1.
//
//go:embed dev-v1.schema.json
var DevV1 []byte
