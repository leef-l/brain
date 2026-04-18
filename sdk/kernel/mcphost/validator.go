// validator.go — MCP tool argument validation.
//
// Phase A: only checks that required fields are present.
// Phase B-7: full JSON Schema draft 2020-12 validation.
//
// Design reference: 35-MCP-backed-Runtime设计.md §4.3
package mcphost

import (
	"encoding/json"
	"fmt"
)

// ValidateMCPArgs validates that the given args satisfy the inputSchema's
// required fields. This is a Phase A "good enough" implementation — it only
// checks for the presence of required keys, not their types or values.
//
// Returns nil if validation passes or if the schema cannot be parsed (fail-open).
func ValidateMCPArgs(schema json.RawMessage, args json.RawMessage) error {
	if len(schema) == 0 || len(args) == 0 {
		return nil
	}

	var schemaDef struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &schemaDef); err != nil {
		return nil // schema parse failure → fail-open
	}

	if len(schemaDef.Required) == 0 {
		return nil
	}

	var argMap map[string]json.RawMessage
	if err := json.Unmarshal(args, &argMap); err != nil {
		return fmt.Errorf("mcphost: args is not a JSON object: %w", err)
	}

	for _, req := range schemaDef.Required {
		if _, ok := argMap[req]; !ok {
			return fmt.Errorf("mcphost: missing required field: %s", req)
		}
	}

	return nil
}
