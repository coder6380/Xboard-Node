package config

import (
	"strings"
	"testing"
)

func TestAutoInstanceIDMultiNodeUsesUnderscore(t *testing.T) {
	cfg := &Config{
		Panel: PanelConfig{
			URL: "https://panel.example.com",
		},
		Nodes: []NodeEntry{
			{NodeID: 101, NodeType: "vless"},
			{NodeID: 202, NodeType: "vmess"},
		},
	}

	id, err := cfg.AutoInstanceID()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(id, ",") {
		t.Fatalf("id should not contain comma: %s", id)
	}
	if !strings.Contains(id, "101_202") {
		t.Fatalf("id should contain 101_202, got %s", id)
	}
}

func TestExpandNodesDeepCopiesDNSEnv(t *testing.T) {
	cfg := &Config{
		Panel: PanelConfig{
			URL: "https://panel.example.com",
		},
		Cert: CertConfig{
			DNSEnv: map[string]string{
				"CF_Token": "original_token",
			},
		},
		Nodes: []NodeEntry{
			{NodeID: 10},
			{NodeID: 20},
		},
	}

	nodes := cfg.ExpandNodes()
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}

	// Mutating node 0's DNSEnv must not affect node 1's DNSEnv or original cfg.Cert.DNSEnv
	nodes[0].Cert.DNSEnv["CF_Token"] = "mutated_token"
	if cfg.Cert.DNSEnv["CF_Token"] != "original_token" {
		t.Fatalf("parent DNSEnv was modified: %v", cfg.Cert.DNSEnv["CF_Token"])
	}
	if nodes[1].Cert.DNSEnv["CF_Token"] != "original_token" {
		t.Fatalf("sibling DNSEnv was modified: %v", nodes[1].Cert.DNSEnv["CF_Token"])
	}
}
