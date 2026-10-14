package singbox

import (
	"encoding/json"
	"os"
	"testing"
)

func BenchmarkMarshalSingBox(b *testing.B) {
	config, err := os.ReadFile("testdata/server.json")
	if err != nil {
		b.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := marshalSingBox(root); err != nil {
			b.Fatal(err)
		}
	}
}
