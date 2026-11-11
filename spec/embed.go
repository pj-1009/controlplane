// Package spec holds the API contract. The file is embedded so tests can
// load it from any package directory.
package spec

import _ "embed"

// OpenAPI is the contents of openapi.yaml.
//
//go:embed openapi.yaml
var OpenAPI []byte
