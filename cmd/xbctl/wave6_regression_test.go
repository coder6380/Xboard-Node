package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cedar2025/xboard-node/internal/config"
	"gopkg.in/yaml.v3"
)

func TestSanitizeEnvKey(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"node-1", "NODE_1"},
		{"node_panel.example.com_10_20", "NODE_PANEL_EXAMPLE_COM_10_20"},
		{"a:b/c.d@e", "A_B_C_D_E"},
		{"123-abc_XYZ", "123_ABC_XYZ"},
	}

	for _, tc := range tests {
		got := sanitizeEnvKey(tc.input)
		if got != tc.expected {
			t.Errorf("sanitizeEnvKey(%q) = %q; want %q", tc.input, got, tc.expected)
		}
	}
}

func TestRemoveBindingMultiNode(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")

	root := &config.RootConfig{
		Instances: []config.Config{
			{
				Panel: config.PanelConfig{
					URL: "https://panel.example.com",
				},
				Nodes: []config.NodeEntry{
					{NodeID: 10, NodeType: "vless"},
					{NodeID: 20, NodeType: "vmess"},
				},
			},
		},
	}
	data, err := yaml.Marshal(root)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatalf("write file error: %v", err)
	}

	// Remove node 10 from the multi-node instance
	rootLoaded, err := loadWritableRootConfig(cfgPath)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	instances := normalizeRootInstances(rootLoaded)
	if len(instances) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(instances))
	}

	var kept []config.Config
	var removed []config.Config
	for _, inst := range instances {
		if stringsEqual(inst.Panel.URL, "https://panel.example.com") && !inst.IsMachineMode() {
			if len(inst.Nodes) > 0 {
				var remainingNodes []config.NodeEntry
				var foundNode bool
				for _, entry := range inst.Nodes {
					if entry.NodeID == 10 {
						foundNode = true
					} else {
						remainingNodes = append(remainingNodes, entry)
					}
				}
				if foundNode {
					removedEntryCfg := inst
					removedEntryCfg.Panel.NodeID = 10
					removed = append(removed, removedEntryCfg)
					if len(remainingNodes) > 0 {
						inst.Nodes = remainingNodes
						kept = append(kept, inst)
					}
					continue
				}
			}
		}
		kept = append(kept, inst)
	}

	if len(removed) != 1 || removed[0].Panel.NodeID != 10 {
		t.Fatalf("expected 1 removed node with ID 10, got %v", removed)
	}
	if len(kept) != 1 || len(kept[0].Nodes) != 1 || kept[0].Nodes[0].NodeID != 20 {
		t.Fatalf("expected remaining node with ID 20, got %v", kept)
	}
}

func stringsEqual(a, b string) bool {
	return a == b
}

func TestConfigInit_TrimsInputsAndNormalizesKernel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")
	credsPath := filepath.Join(dir, "credentials.env")
	metaPath := filepath.Join(dir, "install-meta.json")

	args := []string{
		"--mode", "node",
		"--panel-url", "  https://panel.example.com/  ",
		"--token", "  supersecret  ",
		"--node-id", "123",
		"--kernel", "sing-box",
		"--output", cfgPath,
		"--credentials-out", credsPath,
		"--meta", metaPath,
		"--install-root", dir,
	}

	if err := runConfigInit(args); err != nil {
		t.Fatalf("runConfigInit failed: %v", err)
	}

	root, err := loadWritableRootConfig(cfgPath)
	if err != nil {
		t.Fatalf("loadWritableRootConfig: %v", err)
	}
	if len(root.Instances) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(root.Instances))
	}
	inst := root.Instances[0]
	if inst.Panel.URL != "https://panel.example.com/" {
		t.Errorf("panel.url not trimmed: %q", inst.Panel.URL)
	}
	if inst.Kernel.Type != "singbox" {
		t.Errorf("kernel.type not normalized to singbox: %q", inst.Kernel.Type)
	}

	// Verify credentials file doesn't have leading/trailing spaces around token
	credData, err := os.ReadFile(credsPath)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	lines, err := credentialEnvironment(credData)
	if err != nil {
		t.Fatalf("parse credentials: %v", err)
	}
	found := false
	for _, line := range lines {
		if stringsEqual(line, inst.Panel.TokenEnv+"=supersecret") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected clean token in credentials, got: %s", string(credData))
	}
}
