package geodata

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsure_NoFilesNeeded(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "geodata")
	err := Ensure(dir, false, false, "singbox")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("directory was not created: %v", err)
	}
}

func TestAtomicDownload(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/test.db" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("mock-geodata-content"))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	dir := t.TempDir()
	dst := filepath.Join(dir, "geoip.db")

	// 首次下载
	err := atomicDownload(dst, ts.URL+"/test.db")
	if err != nil {
		t.Fatalf("atomicDownload failed: %v", err)
	}
	content, err := os.ReadFile(dst)
	if err != nil || string(content) != "mock-geodata-content" {
		t.Fatalf("unexpected content: %s, err: %v", content, err)
	}

	// 覆盖已存在的目标文件 (测试 Windows 平台兼容性)
	err = atomicDownload(dst, ts.URL+"/test.db")
	if err != nil {
		t.Fatalf("atomicDownload overwrite failed: %v", err)
	}

	// HTTP 404 错误处理
	err = atomicDownload(filepath.Join(dir, "notfound.db"), ts.URL+"/notfound")
	if err == nil {
		t.Fatal("expected error on 404, got nil")
	}
}

func TestEnsureFile_AlreadyExists(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "geoip.db")
	if err := os.WriteFile(dst, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	// 传入非法 URL，如果文件已存在则不应发起请求
	err := ensureFile(dir, "geoip.db", "http://invalid.invalid/never")
	if err != nil {
		t.Fatalf("expected nil when file exists, got: %v", err)
	}
}

func TestLockForFile(t *testing.T) {
	l1 := lockForFile("a/b/c")
	l2 := lockForFile("a/b/c")
	if l1 != l2 {
		t.Fatal("lockForFile did not return the same mutex for the same path")
	}
}
