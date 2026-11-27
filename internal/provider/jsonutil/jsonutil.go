package jsonutil

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func Marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// ParseOptionalObject reads an optional object without discarding other fields.
// A missing or null field is returned as nil so callers can apply core defaults.
func ParseOptionalObject(config []byte, core, operation, key string) (map[string]any, map[string]any, error) {
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		return nil, nil, fmt.Errorf("%s现有 %s 配置: %w", operation, core, err)
	}
	if root == nil {
		return nil, nil, fmt.Errorf("%s现有 %s 配置: 顶层不是 JSON 对象", operation, core)
	}
	raw := root[key]
	if raw == nil {
		return root, nil, nil
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("现有 %s %s 不是对象", core, key)
	}
	return root, object, nil
}
