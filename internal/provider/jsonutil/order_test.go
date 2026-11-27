package jsonutil

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestMarshalOrderedPreservesExtensionsAndInput(t *testing.T) {
	root := map[string]any{
		"z-custom": "<tag>&value",
		"a-custom": json.Number("9007199254740993"),
		"items": []any{map[string]any{
			"z":     []string{"<one>", "two"},
			"first": true,
			"a":     json.RawMessage(`{"html":"<custom>&"}`),
		}},
		"name": "example",
	}
	before, err := Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := MarshalOrdered(root, func(map[string]any) []string {
		return []string{"missing", "name", "items", "first", "name"}
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "name": "example",
  "items": [
    {
      "first": true,
      "a": {
        "html": "<custom>&"
      },
      "z": [
        "<one>",
        "two"
      ]
    }
  ],
  "a-custom": 9007199254740993,
  "z-custom": "<tag>&value"
}
`
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	after, err := Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("marshaling changed the input configuration")
	}
}

func TestMarshalOrderedKeepsFieldRulesIndependent(t *testing.T) {
	root := map[string]any{"a": 1, "b": 2}
	for _, order := range [][]string{{"a", "b"}, {"b", "a"}} {
		got, err := MarshalOrdered(root, func(map[string]any) []string { return order })
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(bytes.NewReader(got))
		if _, err := decoder.Token(); err != nil {
			t.Fatal(err)
		}
		for _, key := range order {
			token, err := decoder.Token()
			if err != nil || token != key {
				t.Fatalf("field = %v, error = %v; want %s", token, err, key)
			}
			var value int
			if err := decoder.Decode(&value); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestMarshalOrderedValues(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value any
		want  string
	}{
		{"nil", nil, "null\n"},
		{"map", map[string]any(nil), "{}\n"},
		{"array", []any(nil), "[]\n"},
		{"typed nil slice", []string(nil), "null\n"},
		{"integer", uint64(9007199254740993), "9007199254740993\n"},
		{"string", "<>&", "\"<>&\"\n"},
		{"unknown fields", map[string]any{"z": 2, "a": 1}, "{\n  \"a\": 1,\n  \"z\": 2\n}\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MarshalOrdered(tt.value, nil)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestMarshalOrderedRejectsInvalidValues(t *testing.T) {
	for _, value := range []any{make(chan int), math.NaN(), json.RawMessage(`invalid`)} {
		for _, root := range []any{value, map[string]any{"nested": []any{value}}} {
			got, err := MarshalOrdered(root, nil)
			if err == nil || got != nil {
				t.Fatalf("value %v: got %s, error %v", reflect.TypeOf(root), got, err)
			}
		}
	}
}
