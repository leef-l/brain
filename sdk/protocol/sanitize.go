// sanitize.go — MACCS Wave 7+ 全局 marshal 防御
//
// 解决问题:LLM 工具调用结果 / sidecar 反传 ExecuteResult 等场景里,
// 结构体字段 json.RawMessage 可能含坏 JSON (LLM 输出未转义反斜杠 / 控制字符
// / 截断 UTF-8),整体 json.Marshal 时报
//   "result marshal failed: json: error calling MarshalJSON for type json.RawMessage"
// 导致 RPC 调用整体失败。
//
// 本 helper 用反射递归扫描结构体,把所有非法 RawMessage 转成占位字符串,
// 让外层 Marshal 能成功完成,失败的字段以 {"_invalid_json": "..."} 形式返回。

package protocol

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// SanitizeForMarshal 接收任意 interface{},递归扫一遍:
//   - json.RawMessage 类型:校验 JSON 合法性,不合法替换为占位
//   - struct/map/slice:递归
//   - 其他类型(string/int/float/bool/...): 原样返回
//
// 返回新值(原值不变,因为反射操作可能不安全)。
//
// 核心思路:json.RawMessage.MarshalJSON 内部会校验内容是否合法 JSON,
// 不合法就报错。我们提前用 json.Valid 判断,坏的换成 string 占位,
// 这样外层 Marshal 能正常进行。
func SanitizeForMarshal(v interface{}) interface{} {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	return sanitizeValue(rv).Interface()
}

func sanitizeValue(rv reflect.Value) reflect.Value {
	if !rv.IsValid() {
		return reflect.ValueOf((*interface{})(nil)).Elem()
	}

	// 解引用 pointer
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return rv
		}
		return rv
	}

	// json.RawMessage 是 []byte 的别名,要专门处理
	if rv.Type() == reflect.TypeOf(json.RawMessage{}) {
		raw := rv.Bytes()
		if len(raw) == 0 || json.Valid(raw) {
			return rv
		}
		// 不合法 → 替换为占位字符串(用 string 包成合法 JSON)
		placeholder := fmt.Sprintf(`{"_invalid_json": true, "_raw_len": %d}`, len(raw))
		return reflect.ValueOf(json.RawMessage(placeholder))
	}

	switch rv.Kind() {
	case reflect.Struct:
		// 创建可写副本
		newStruct := reflect.New(rv.Type()).Elem()
		newStruct.Set(rv)
		for i := 0; i < newStruct.NumField(); i++ {
			f := newStruct.Field(i)
			if !f.CanSet() {
				continue
			}
			cleaned := sanitizeValue(f)
			if cleaned.IsValid() && cleaned.Type().AssignableTo(f.Type()) {
				f.Set(cleaned)
			}
		}
		return newStruct

	case reflect.Map:
		if rv.IsNil() {
			return rv
		}
		newMap := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			k := iter.Key()
			v := iter.Value()
			cleaned := sanitizeValue(v)
			if cleaned.IsValid() {
				if cleaned.Type().AssignableTo(rv.Type().Elem()) {
					newMap.SetMapIndex(k, cleaned)
				} else {
					newMap.SetMapIndex(k, v) // 类型不兼容,保留原值
				}
			}
		}
		return newMap

	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return rv
		}
		// []byte / []uint8 不递归(基础类型)
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return rv
		}
		newSlice := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
		for i := 0; i < rv.Len(); i++ {
			cleaned := sanitizeValue(rv.Index(i))
			if cleaned.IsValid() && cleaned.Type().AssignableTo(rv.Type().Elem()) {
				newSlice.Index(i).Set(cleaned)
			} else {
				newSlice.Index(i).Set(rv.Index(i))
			}
		}
		return newSlice

	case reflect.Interface:
		if rv.IsNil() {
			return rv
		}
		inner := rv.Elem()
		cleaned := sanitizeValue(inner)
		if cleaned.IsValid() {
			return cleaned
		}
		return rv

	default:
		// 基础类型直接返回
		return rv
	}
}
