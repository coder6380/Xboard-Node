package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cedar2025/xboard-node/internal/config"
)

func TestConfigInitSecondInstanceClearsDuplicateHealthPort(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")
	credPath := filepath.Join(dir, "credentials.env")
	metaPath := filepath.Join(dir, "install-meta.json")

	// First instance
	if err := runConfigInit([]string{
		"--config", cfgPath, "--output", cfgPath,
		"--credentials-in", credPath, "--credentials-out", credPath,
		"--meta", metaPath,
		"--mode", "node", "--panel-url", "https://panel-a.example.com",
		"--node-id", "1", "--token", "tok1", "--health-port", "65530",
		"--install-root", dir,
	}); err != nil {
		t.Fatalf("first init: %v", err)
	}

	// Second instance also passed default --health-port 65530 (like install.sh does)
	if err := runConfigInit([]string{
		"--config", cfgPath, "--output", cfgPath,
		"--credentials-in", credPath, "--credentials-out", credPath,
		"--meta", metaPath,
		"--mode", "node", "--panel-url", "https://panel-b.example.com",
		"--node-id", "2", "--token", "tok2", "--health-port", "65530",
		"--install-root", dir,
	}); err != nil {
		t.Fatalf("second init: %v", err)
	}

	root, err := loadWritableRootConfig(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(root.Instances) != 2 {
		t.Fatalf("instances = %d, want 2", len(root.Instances))
	}
	if root.Instances[0].HealthPort != 65530 || root.Instances[1].HealthPort != 0 {
		t.Fatalf("health ports = [%d, %d], want [65530, 0]", root.Instances[0].HealthPort, root.Instances[1].HealthPort)
	}

	// Verify startup layout passes (no duplicate health_port error)
	ptrs := []*config.Config{&root.Instances[0], &root.Instances[1]}
	if err := config.ValidateStartupLayout(ptrs); err != nil {
		t.Fatalf("ValidateStartupLayout failed: %v", err)
	}
}

func TestConfigInitRejectsCorruptExistingConfigAndUnknownFlags(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(cfgPath, []byte("instances: [broken: yaml\n"), 0o600); err != nil {
		t.Fatalf("write corrupt config: %v", err)
	}

	err := runConfigInit([]string{
		"--config", cfgPath, "--output", cfgPath,
		"--mode", "node", "--panel-url", "https://panel.example.com",
		"--node-id", "1", "--token", "tok",
	})
	if err == nil {
		t.Fatal("expected error when existing config.yml is corrupt instead of overwriting")
	}

	if err := runConfigInit([]string{"--unknown-flag", "1"}); err == nil {
		t.Fatal("expected error for unknown flag")
	}
	if _, err := parsePositiveInt("12abc", "node-id"); err == nil {
		t.Fatal("expected parsePositiveInt to reject trailing garbage")
	}
}

func TestCredentialPruneKeepsSharedKeysAndMergePreservesSpaces(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials.env")
	if err := mergeCredentials("", credPath, "SHARED_KEY", "secret with trailing "); err != nil {
		t.Fatalf("mergeCredentials 1: %v", err)
	}
	if err := mergeCredentials(credPath, credPath, "OTHER_KEY", "val2"); err != nil {
		t.Fatalf("mergeCredentials 2: %v", err)
	}
	raw, _ := os.ReadFile(credPath)
	if !strings.Contains(string(raw), "SHARED_KEY=secret with trailing \n") {
		t.Fatalf("mergeCredentials stripped trailing whitespace: %q", string(raw))
	}

	removed := []config.Config{{Panel: config.PanelConfig{TokenEnv: "SHARED_KEY"}}}
	kept := []config.Config{{Panel: config.PanelConfig{TokenEnv: "SHARED_KEY"}}}
	if err := pruneCredentialKeys(credPath, removed, kept); err != nil {
		t.Fatalf("pruneCredentialKeys: %v", err)
	}
	rawAfter, _ := os.ReadFile(credPath)
	if !strings.Contains(string(rawAfter), "SHARED_KEY=") {
		t.Fatalf("pruneCredentialKeys deleted key still referenced by kept instance: %q", string(rawAfter))
	}

	merged := mergeEnv([]string{"A=old", "B=keep"}, []string{"A=new", "C=added"})
	got := map[string]string{}
	for _, kv := range merged {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	if got["A"] != "new" || got["B"] != "keep" || got["C"] != "added" {
		t.Fatalf("mergeEnv unexpected result: %v", merged)
	}
}
