package domain

import "testing"

func TestConfigDefaults(t *testing.T) {
	if DefaultUserName != "one" {
		t.Fatalf("DefaultUserName = %q, want %q", DefaultUserName, "one")
	}

	tests := []struct {
		core string
		want string
	}{
		{core: CoreXray, want: "xray-one"},
		{core: CoreSingBox, want: "singbox-one"},
	}
	for _, tt := range tests {
		t.Run(tt.core, func(t *testing.T) {
			if got := DefaultInboundTag(tt.core); got != tt.want {
				t.Fatalf("DefaultInboundTag(%q) = %q, want %q", tt.core, got, tt.want)
			}
		})
	}
}
