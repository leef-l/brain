package manifest

import "fmt"

// ValidationError 校验错误
type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("manifest 校验失败: %s — %s", e.Field, e.Message)
}

// Validate 执行完整校验，返回所有校验错误
func Validate(m *Manifest) []ValidationError {
	var errs []ValidationError

	if m.SchemaVersion < 1 {
		errs = append(errs, ValidationError{
			Field:   "schema_version",
			Message: "必须 >= 1",
		})
	}

	if m.Kind == "" {
		errs = append(errs, ValidationError{
			Field:   "kind",
			Message: "不能为空",
		})
	}

	if m.Name == "" {
		errs = append(errs, ValidationError{
			Field:   "name",
			Message: "不能为空",
		})
	}

	if m.BrainVersion == "" {
		errs = append(errs, ValidationError{
			Field:   "brain_version",
			Message: "不能为空",
		})
	}

	if len(m.Capabilities) == 0 {
		errs = append(errs, ValidationError{
			Field:   "capabilities",
			Message: "至少需要一个 capability",
		})
	}

	if !validRuntimeTypes[m.Runtime.Type] {
		errs = append(errs, ValidationError{
			Field:   "runtime.type",
			Message: fmt.Sprintf("无效的运行时类型: %q，有效值: native, mcp-backed, hybrid, wasm, docker, remote", m.Runtime.Type),
		})
	}

	// 特定运行时类型约束
	switch m.Runtime.Type {
	case RuntimeMCPBacked:
		if len(m.Runtime.MCPServers) == 0 {
			errs = append(errs, ValidationError{
				Field:   "runtime.mcp_servers",
				Message: "mcp-backed 类型必须配置至少一个 mcp_server",
			})
		}
	case RuntimeNative:
		if m.Runtime.Entrypoint == "" {
			errs = append(errs, ValidationError{
				Field:   "runtime.entrypoint",
				Message: "native 类型必须指定 entrypoint",
			})
		}
	case RuntimeHybrid:
		if m.Runtime.Entrypoint == "" {
			errs = append(errs, ValidationError{
				Field:   "runtime.entrypoint",
				Message: "hybrid 类型必须指定 entrypoint（native 部分）",
			})
		}
	case RuntimeRemote:
		if m.Runtime.Endpoint == "" {
			errs = append(errs, ValidationError{
				Field:   "runtime.endpoint",
				Message: "remote 类型必须指定 endpoint",
			})
		}
	}

	return errs
}
