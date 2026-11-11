// Package openapi embeds the authoritative API specification (D6).
package openapi

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"go.yaml.in/yaml/v3"
)

// YAML is api/openapi.yaml as written.
//
//go:embed openapi.yaml
var YAML []byte

// JSON converts the specification to JSON for serving at
// /api/v1/openapi.json.
func JSON() ([]byte, error) {
	var doc any
	if err := yaml.Unmarshal(YAML, &doc); err != nil {
		return nil, fmt.Errorf("parse embedded spec: %w", err)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("convert embedded spec to JSON: %w", err)
	}
	return out, nil
}
