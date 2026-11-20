package kernel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCustomConfigNormalizesYAMLNonStringKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yaml")
	content := "dns:\n  rules:\n    - 0: foo\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	cfg, err := LoadCustomConfig(path)
	if err != nil {
		t.Fatalf("LoadCustomConfig: %v", err)
	}
	if _, err := json.Marshal(cfg); err != nil {
		t.Fatalf("json.Marshal failed after YAML normalization: %v", err)
	}
}

func TestMergePrependListDoesNotAliasCallerSlice(t *testing.T) {
	src := make([]map[string]any, 1, 4)
	src[0] = map[string]any{"tag": "a"}
	dst := []map[string]any{{"tag": "b"}}
	merged := MergePrependList(dst, src)
	if len(merged) != 2 {
		t.Fatalf("merged len = %d, want 2", len(merged))
	}
	// Mutating merged must not affect src's backing array capacity elements.
	merged[1] = map[string]any{"tag": "mutated"}
	if len(src) != 1 || src[0]["tag"] != "a" {
		t.Fatalf("src corrupted: %#v", src)
	}
	extended := append(src, map[string]any{"tag": "c"})
	if extended[1]["tag"] != "c" {
		t.Fatalf("src backing array aliased by MergePrependList: %#v", extended)
	}
}
