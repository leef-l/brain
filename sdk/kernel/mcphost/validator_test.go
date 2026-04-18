package mcphost

import (
	"encoding/json"
	"testing"
)

func TestValidateMCPArgs_AllPresent(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
	args := json.RawMessage(`{"path":"/tmp/test"}`)
	if err := ValidateMCPArgs(schema, args); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateMCPArgs_MissingRequired(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
	args := json.RawMessage(`{"other":"value"}`)
	if err := ValidateMCPArgs(schema, args); err == nil {
		t.Fatal("expected error for missing required field")
	}
}

func TestValidateMCPArgs_NoRequired(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
	args := json.RawMessage(`{}`)
	if err := ValidateMCPArgs(schema, args); err != nil {
		t.Fatalf("no required fields, should pass: %v", err)
	}
}

func TestValidateMCPArgs_EmptySchema(t *testing.T) {
	if err := ValidateMCPArgs(nil, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("nil schema should pass: %v", err)
	}
	if err := ValidateMCPArgs(json.RawMessage(``), json.RawMessage(`{}`)); err != nil {
		t.Fatalf("empty schema should pass: %v", err)
	}
}

func TestValidateMCPArgs_EmptyArgs(t *testing.T) {
	schema := json.RawMessage(`{"required":["path"]}`)
	if err := ValidateMCPArgs(schema, nil); err != nil {
		t.Fatalf("nil args should pass: %v", err)
	}
}

func TestValidateMCPArgs_InvalidArgsJSON(t *testing.T) {
	schema := json.RawMessage(`{"required":["path"]}`)
	args := json.RawMessage(`not json`)
	if err := ValidateMCPArgs(schema, args); err == nil {
		t.Fatal("expected error for invalid args JSON")
	}
}

func TestValidateMCPArgs_InvalidSchemaJSON(t *testing.T) {
	// Invalid schema JSON should fail-open.
	schema := json.RawMessage(`not json`)
	args := json.RawMessage(`{"path":"test"}`)
	if err := ValidateMCPArgs(schema, args); err != nil {
		t.Fatalf("invalid schema should fail-open: %v", err)
	}
}

func TestValidateMCPArgs_MultipleRequired(t *testing.T) {
	schema := json.RawMessage(`{"required":["path","content"]}`)

	// Both present.
	args := json.RawMessage(`{"path":"/a","content":"hello"}`)
	if err := ValidateMCPArgs(schema, args); err != nil {
		t.Fatalf("both present should pass: %v", err)
	}

	// Only one present.
	args = json.RawMessage(`{"path":"/a"}`)
	if err := ValidateMCPArgs(schema, args); err == nil {
		t.Fatal("missing content should fail")
	}
}
