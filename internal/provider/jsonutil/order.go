package jsonutil

import (
	"bytes"
	"encoding/json"
	"slices"
)

// MarshalOrdered formats JSON using provider-defined field orders. The callback
// is applied to each map[string]any, including objects inside []any. Fields not
// listed by the callback are retained in alphabetical order. Other values use
// encoding/json, preserving typed values and custom JSON marshalers.
func MarshalOrdered(value any, fieldOrder func(map[string]any) []string) ([]byte, error) {
	var raw bytes.Buffer
	writer := orderedWriter{output: &raw, encoder: json.NewEncoder(&raw), fieldOrder: fieldOrder}
	writer.encoder.SetEscapeHTML(false)
	if err := writer.write(value); err != nil {
		return nil, err
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, raw.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	formatted.WriteByte('\n')
	return formatted.Bytes(), nil
}

type orderedWriter struct {
	output     *bytes.Buffer
	encoder    *json.Encoder
	fieldOrder func(map[string]any) []string
}

func (w *orderedWriter) write(value any) error {
	switch value := value.(type) {
	case map[string]any:
		var order []string
		if w.fieldOrder != nil {
			order = w.fieldOrder(value)
		}
		keys := make([]string, 0, len(value))
		for _, key := range order {
			if _, exists := value[key]; exists && !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}
		known := len(keys)
		for key := range value {
			if !slices.Contains(order, key) {
				keys = append(keys, key)
			}
		}
		slices.Sort(keys[known:])
		w.output.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				w.output.WriteByte(',')
			}
			if err := w.write(key); err != nil {
				return err
			}
			w.output.WriteByte(':')
			if err := w.write(value[key]); err != nil {
				return err
			}
		}
		w.output.WriteByte('}')
	case []any:
		w.output.WriteByte('[')
		for index, item := range value {
			if index > 0 {
				w.output.WriteByte(',')
			}
			if err := w.write(item); err != nil {
				return err
			}
		}
		w.output.WriteByte(']')
	default:
		if err := w.encoder.Encode(value); err != nil {
			return err
		}
		// Encode appends a newline; only the completed document needs one.
		w.output.Truncate(w.output.Len() - 1)
	}
	return nil
}
