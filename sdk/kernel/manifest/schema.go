// schema.go — Manifest JSON Schema 嵌入
//
// schema/v1.json 通过 go embed 内嵌进二进制,供:
//   1. Manifest 加载时做可选 schema 校验(非默认,validator.go 是主校验)
//   2. brain doctor / IDE 工具读取做 brain.json 自动补全提示
//   3. Marketplace 发布时附带 schema URL 供第三方校验

package manifest

import _ "embed"

//go:embed schema/v1.json
var schemaV1JSON []byte

// SchemaV1 返回 v1 JSON Schema 原文(只读)。
//
// 调用方拿到后可:
//   - 走 github.com/santhosh-tekuri/jsonschema 等库做严格校验
//   - 写入 stdout 给 IDE/CLI 消费
//   - 上传到 Marketplace 元数据
//
// 注意:本包的 Validate(m) 是基于代码字段的快速校验,不依赖 schema。
// schema 只是规格的 JSON 形态,与 types.go 字段保持同步(改 types.go 也要改 schema/v1.json)。
func SchemaV1() []byte {
	out := make([]byte, len(schemaV1JSON))
	copy(out, schemaV1JSON)
	return out
}
