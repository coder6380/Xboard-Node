package cert

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/caddyserver/certmagic"
	"github.com/cedar2025/xboard-node/internal/config"
)

// TestSharedACMEStorage 验证同域共享证书且节点 PEM 互不覆盖。
func TestSharedACMEStorage(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "shared")
	a := NewManager(config.CertConfig{CertDir: filepath.Join(root, "a"), ACMEStorageDir: shared})
	b := NewManager(config.CertConfig{CertDir: filepath.Join(root, "b"), ACMEStorageDir: shared})
	certPEM, keyPEM := generateSelfSignedPair(t, "example.test")
	ctx := context.Background()
	const issuer, domain = "test-issuer", "example.test"
	storage := a.acmeStorage()
	if err := storage.Store(ctx, certmagic.StorageKeys.SiteCert(issuer, domain), certPEM); err != nil {
		t.Fatal(err)
	}
	if err := storage.Store(ctx, certmagic.StorageKeys.SitePrivateKey(issuer, domain), keyPEM); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*Manager{a, b} {
		if err := m.loadPEMFromStorage(ctx, m.acmeStorage(), issuer, domain); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.loadPEMFromStorage(ctx, b.acmeStorage(), issuer, "other.test"); err == nil {
		t.Fatal("不同域名不应读取同一证书")
	}
	otherCert, otherKey := generateSelfSignedPair(t, "example.test")
	a.storePEM(otherCert, otherKey)
	got, err := os.ReadFile(filepath.Join(b.cfg.CertDir, "cert.pem"))
	if err != nil || string(got) != string(certPEM) {
		t.Fatalf("另一节点 PEM 被修改: %v", err)
	}
	if got := NewManager(config.CertConfig{CertDir: root}).acmeStorage().Path; got != root {
		t.Fatalf("默认存储路径错误: %q", got)
	}
}
