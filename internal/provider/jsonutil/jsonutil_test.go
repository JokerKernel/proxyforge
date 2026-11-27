package jsonutil

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseOptionalObject(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		absent       bool
		invalid      bool
	}{
		{name: "missing", config: `{"manual":true}`, absent: true},
		{name: "null", config: `{"manual":true,"log":null}`, absent: true},
		{name: "object", config: `{"manual":true,"log":{"custom":"keep"}}`},
		{name: "scalar", config: `{"log":false}`, invalid: true},
		{name: "array", config: `{"log":[]}`, invalid: true},
		{name: "null-root", config: `null`, invalid: true},
		{name: "array-root", config: `[]`, invalid: true},
		{name: "invalid-json", config: `{"log":`, invalid: true},
		{name: "trailing-json", config: `{} {}`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, object, err := ParseOptionalObject([]byte(tc.config), "Xray", "读取", "log")
			if tc.invalid {
				if err == nil || !strings.Contains(err.Error(), "Xray") {
					t.Fatalf("expected contextual error: %v", err)
				}
				if tc.name == "invalid-json" {
					var syntaxErr *json.SyntaxError
					if !errors.As(err, &syntaxErr) {
						t.Fatalf("JSON error chain lost: %v", err)
					}
				}
				return
			}
			if err != nil || root["manual"] != true || (object == nil) != tc.absent {
				t.Fatalf("unexpected result: root=%v object=%v err=%v", root, object, err)
			}
			if !tc.absent && object["custom"] != "keep" {
				t.Fatalf("manual field lost: %v", object)
			}
		})
	}
}
