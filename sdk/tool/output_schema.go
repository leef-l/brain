package tool

import "encoding/json"

var (
	dynamicJSONOutputSchema = json.RawMessage(`true`)

	readFileOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "content": { "type": "string" },
    "lines": { "type": "integer" },
    "total_lines": { "type": "integer" },
    "truncated": { "type": "boolean" },
    "path": { "type": "string" }
  },
  "required": ["content", "lines", "total_lines", "truncated", "path"]
}`)

	writeFileOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "bytes_written": { "type": "integer" },
    "path": { "type": "string" }
  },
  "required": ["bytes_written", "path"]
}`)

	editFileOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":          { "type": "string" },
    "replacements":  { "type": "integer" },
    "bytes_written": { "type": "integer" }
  },
  "required": ["path", "replacements", "bytes_written"]
}`)

	listFilesOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "paths":     { "type": "array", "items": { "type": "string" } },
    "count":     { "type": "integer" },
    "truncated": { "type": "boolean" }
  },
  "required": ["paths", "count", "truncated"]
}`)

	noteOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": { "type": "string" },
    "ok":     { "type": "boolean" },
    "id":     { "type": "integer" },
    "items":  { "type": "array" },
    "note":   { "type": "string" }
  },
  "required": ["action", "ok"]
}`)

	deleteFileOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "deleted": { "type": "boolean" },
    "path": { "type": "string" }
  },
  "required": ["deleted", "path"]
}`)

	searchOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "matches": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "file": { "type": "string" },
          "line": { "type": "integer" },
          "text": { "type": "string" }
        },
        "required": ["file", "line", "text"]
      }
    },
    "total": { "type": "integer" },
    "truncated": { "type": "boolean" }
  },
  "required": ["matches", "total", "truncated"]
}`)

	shellExecOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "stdout": { "type": "string" },
    "stderr": { "type": "string" },
    "exit_code": { "type": "integer" },
    "timed_out": { "type": "boolean" },
    "sandboxed": { "type": "boolean" }
  },
  "required": ["stdout", "stderr", "exit_code", "timed_out", "sandboxed"]
}`)

	runTestsOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "stdout": { "type": "string" },
    "stderr": { "type": "string" },
    "exit_code": { "type": "integer" },
    "passed": { "type": "boolean" },
    "timed_out": { "type": "boolean" }
  },
  "required": ["stdout", "stderr", "exit_code", "passed", "timed_out"]
}`)

	checkOutputResultSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "match": { "type": "boolean" },
    "mode": { "type": "string" },
    "diff": { "type": "string" }
  },
  "required": ["match", "mode"]
}`)

	rejectTaskOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "status": { "const": "rejected" },
    "reason": { "type": "string" }
  },
  "required": ["status", "reason"]
}`)

	browserOpenOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "status": { "type": "string" },
    "url": { "type": "string" },
    "target_id": { "type": "string" }
  },
  "required": ["status", "url", "target_id"]
}`)

	browserNavigateOutputSchema = json.RawMessage(`{
  "oneOf": [
    {
      "type": "object",
      "properties": {
        "status": { "type": "string" },
        "action": { "type": "string" }
      },
      "required": ["status", "action"]
    },
    {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "id": { "type": "string" },
          "type": { "type": "string" },
          "title": { "type": "string" },
          "url": { "type": "string" },
          "webSocketDebuggerUrl": { "type": "string" }
        },
        "required": ["id", "type", "title", "url"]
      }
    }
  ]
}`)

	browserPointOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "status": { "type": "string" },
    "x": { "type": "number" },
    "y": { "type": "number" }
  },
  "required": ["status", "x", "y"]
}`)

	browserTypeOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "status": { "type": "string" },
    "typed": { "type": "integer" }
  },
  "required": ["status", "typed"]
}`)

	browserKeyOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "status": { "type": "string" },
    "key": { "type": "string" }
  },
  "required": ["status", "key"]
}`)

	browserScrollOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "status": { "type": "string" },
    "delta_x": { "type": "number" },
    "delta_y": { "type": "number" }
  },
  "required": ["status", "delta_x", "delta_y"]
}`)

	browserDragOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "status": { "type": "string" },
    "from": {
      "type": "array",
      "items": { "type": "number" },
      "minItems": 2,
      "maxItems": 2
    },
    "to": {
      "type": "array",
      "items": { "type": "number" },
      "minItems": 2,
      "maxItems": 2
    }
  },
  "required": ["status", "from", "to"]
}`)

	browserSelectOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "status": { "type": "string" },
    "value": { "type": "string" },
    "text": { "type": "string" },
    "index": { "type": "integer" }
  },
  "required": ["status"]
}`)

	browserUploadOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "status": { "type": "string" },
    "files": {
      "type": "array",
      "items": { "type": "string" }
    }
  },
  "required": ["status", "files"]
}`)

	browserScreenshotOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "status": { "type": "string" },
    "format": { "type": "string" },
    "data": { "type": "string", "description": "Base64-encoded image payload" },
    "encoding": { "const": "base64" }
  },
  "required": ["status", "format", "data", "encoding"]
}`)

	browserEvalOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "type": { "type": "string" },
    "value": {}
  },
  "required": ["type", "value"]
}`)

	browserWaitOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "status": { "type": "string" },
    "condition": { "type": "string" }
  },
  "required": ["status", "condition"]
}`)

	faultInjectErrorOutputSchema = json.RawMessage(`{
  "oneOf": [
    {
      "type": "object",
      "properties": {
        "type": { "const": "file_corrupt" },
        "original": { "type": "string" },
        "corrupted_file": { "type": "string" },
        "bytes_corrupted": { "type": "integer" },
        "original_size": { "type": "integer" }
      },
      "required": ["type", "original", "corrupted_file", "bytes_corrupted", "original_size"]
    },
    {
      "type": "object",
      "properties": {
        "type": { "const": "env_poison" },
        "command": { "type": "string" },
        "env_key": { "type": "string" },
        "env_value": { "type": "string" },
        "exit_code": { "type": "integer" },
        "stdout": { "type": "string" },
        "stderr": { "type": "string" }
      },
      "required": ["type", "command", "env_key", "env_value", "exit_code", "stdout", "stderr"]
    },
    {
      "type": "object",
      "properties": {
        "type": { "const": "exit_code" },
        "command": { "type": "string" },
        "real_exit_code": { "type": "integer" },
        "stdout": { "type": "string" },
        "stderr": { "type": "string" },
        "note": { "type": "string" }
      },
      "required": ["type", "command", "real_exit_code", "stdout", "stderr", "note"]
    },
    {
      "type": "object",
      "properties": {
        "type": { "const": "disk_full" },
        "file": { "type": "string" },
        "written_mb": { "type": "integer" },
        "simulated": { "type": "boolean" },
        "error": { "type": "string" },
        "note": { "type": "string" }
      },
      "required": ["type", "file", "written_mb"]
    }
  ]
}`)

	faultInjectLatencyOutputSchema = json.RawMessage(`{
  "oneOf": [
    {
      "type": "object",
      "properties": {
        "type": { "const": "startup_latency" },
        "injected_delay_ms": { "type": "integer" },
        "total_elapsed_ms": { "type": "integer" },
        "exit_code": { "type": "integer" },
        "stdout": { "type": "string" },
        "stderr": { "type": "string" }
      },
      "required": ["type", "injected_delay_ms", "total_elapsed_ms", "exit_code", "stdout", "stderr"]
    },
    {
      "type": "object",
      "properties": {
        "type": { "const": "network_latency" },
        "delay_ms": { "type": "integer" },
        "jitter_ms": { "type": "integer" },
        "tc_add": { "type": "string" },
        "tc_remove": { "type": "string" },
        "note": { "type": "string" }
      },
      "required": ["type", "delay_ms", "jitter_ms", "tc_add", "tc_remove", "note"]
    }
  ]
}`)

	faultKillProcessOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "type": { "const": "kill_process" },
    "signal": { "type": "string" },
    "processes": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "pid": { "type": "integer" },
          "status": { "type": "string" },
          "signal": { "type": "string" },
          "error": { "type": "string" }
        },
        "required": ["pid", "status"]
      }
    }
  },
  "required": ["type", "signal", "processes"]
}`)

	faultCorruptResponseOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "type": { "const": "corrupt_response" },
    "mode": { "type": "string" },
    "exit_code": { "type": "integer" },
    "original_length": { "type": "integer" },
    "corrupted": { "type": "string" },
    "original": { "type": "string" }
  },
  "required": ["type", "mode", "exit_code", "original_length", "corrupted", "original"]
}`)
)
