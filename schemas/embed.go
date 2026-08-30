// Package schemas は dev.yml のJSON Schemaをバイナリへ同梱する。
//
// devkitが同梱するアセットはこのSchemaのみである（仕様 02-repository-layout.md 2.4）。
package schemas

import _ "embed"

// DevV1 は schema: 1 の JSON Schema。
//
//go:embed dev-v1.schema.json
var DevV1 []byte
