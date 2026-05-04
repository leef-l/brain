// converter.go — Manifest schema 版本转换
//
// 目的:为未来 manifest schema 演进留好转换层。当出现 v2 格式时,旧 brain.json
// 可以通过 ConvertToLatest 自动迁移,而无需第三方手动改文件。
//
// 当前状态:
//   - 唯一支持的 schema 是 v1
//   - ConvertToLatest 接收 raw JSON / 已解析 Manifest,根据 schema_version 字段
//     选择转换链;v1 → 直通,无字段重命名 / 拆分 / 合并
//
// 演进协议:
//   - 新增 v2 时:
//     1. types.go 加新字段(向后兼容,旧字段保留 + omitempty)
//     2. 这里加 convertV1ToV2 函数,处理重命名 / 默认值填充 / 弃用字段映射
//     3. ConvertToLatest 内部根据 schema_version 路由
//   - 不破坏向后兼容(老 brain.json 永远能加载,即便字段被弃用)

package manifest

import (
	"encoding/json"
	"fmt"
)

// LatestSchemaVersion 当前包支持的最新 schema 版本号。
const LatestSchemaVersion = 1

// ConvertToLatest 把任意 schema 版本的 raw JSON 升级到 LatestSchemaVersion。
//
// 输入:raw JSON 字节流。函数先 peek schema_version,据此选择转换链。
// 输出:解析好的 Manifest(已是最新版结构),或转换错误。
//
// 特殊情况:
//   - schema_version 缺失或 0 → 视为 v1 解析(向后兼容旧 brain.json)
//   - schema_version > LatestSchemaVersion → 报错(本 kernel 太老,不能识别)
//   - schema_version < 1 → 报错
func ConvertToLatest(raw []byte) (*Manifest, error) {
	var peek struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		return nil, fmt.Errorf("manifest: peek schema_version: %w", err)
	}

	v := peek.SchemaVersion
	if v == 0 {
		v = 1 // 缺失字段视为 v1
	}
	if v < 1 {
		return nil, fmt.Errorf("manifest: invalid schema_version %d (must be >= 1)", v)
	}
	if v > LatestSchemaVersion {
		return nil, fmt.Errorf(
			"manifest: schema_version %d is newer than this kernel supports (max %d). Upgrade brain CLI",
			v, LatestSchemaVersion,
		)
	}

	// 当前唯一路径:v == 1,直通。
	// 未来 v2 加入时,在此之前先 if v == 1 { raw = convertV1ToV2(raw); v = 2 }。
	m := &Manifest{}
	if err := json.Unmarshal(raw, m); err != nil {
		return nil, fmt.Errorf("manifest: parse v%d: %w", v, err)
	}
	if m.SchemaVersion == 0 {
		m.SchemaVersion = 1
	}
	return m, nil
}

// MigrateInPlace 把已解析的 *Manifest 升级到最新版(对未来 v2+ 有用,v1 是 no-op)。
//
// 用途:第三方代码先用 LoadFromFile 拿到 Manifest 后,再调本函数确保是最新版字段集。
// LoadFromFile 内部默认解析为最新版结构,所以 v1 的 MigrateInPlace 不会改动任何字段。
func MigrateInPlace(m *Manifest) error {
	if m == nil {
		return fmt.Errorf("manifest: nil manifest")
	}
	if m.SchemaVersion == 0 {
		m.SchemaVersion = 1
	}
	if m.SchemaVersion > LatestSchemaVersion {
		return fmt.Errorf(
			"manifest: schema_version %d is newer than this kernel supports (max %d)",
			m.SchemaVersion, LatestSchemaVersion,
		)
	}
	// v1 → 直通,无字段迁移
	return nil
}
