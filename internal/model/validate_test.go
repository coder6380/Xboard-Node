package model

import (
	"testing"
)

func TestResolveKernelForTransport(t *testing.T) {
	tests := []struct {
		network    string
		configured string
		want       string
	}{
		{"xhttp", "singbox", "xray"},
		{"xhttp", "sing-box", "xray"},
		{"splithttp", "singbox", "xray"},
		{"h2", "xray", "singbox"},
		{"h2", "xray-core", "singbox"},
		{"http", "xray", "singbox"},
		{"quic", "xray", "singbox"},
		{"h3", "xray", "singbox"},
		{"tcp", "singbox", "singbox"},
		{"tcp", "xray", "xray"},
		{"ws", "singbox", "singbox"},
		{"ws", "xray", "xray"},
	}

	for _, tt := range tests {
		got := ResolveKernelForTransport(tt.network, tt.configured)
		if got != tt.want {
			t.Errorf("ResolveKernelForTransport(%q, %q) = %q, want %q", tt.network, tt.configured, got, tt.want)
		}
	}
}

func TestNormalizeKernelType(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"singbox", "singbox", false},
		{"sing-box", "singbox", false},
		{"Sing-Box", "singbox", false},
		{"xray", "xray", false},
		{"xray-core", "xray", false},
		{"Xray-Core", "xray", false},
		{"unknown", "", true},
	}

	for _, tt := range tests {
		got, err := normalizeKernelType(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("normalizeKernelType(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("normalizeKernelType(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
