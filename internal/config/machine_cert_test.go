package config

import (
	"path/filepath"
	"testing"
)

// TestMachineCertificateStorage 验证机器节点共享 ACME 状态但隔离 PEM。
func TestMachineCertificateStorage(t *testing.T) {
	base := &Config{Machine: &MachineConfig{}}
	base.Kernel.ConfigDir = t.TempDir()
	a := base.ExpandMachineNode(1, "trojan")
	b := base.ExpandMachineNode(2, "trojan")
	if a.Cert.CertDir == b.Cert.CertDir {
		t.Fatal("节点 PEM 目录不应相同")
	}
	want := filepath.Join(base.Kernel.ConfigDir, "acme-shared")
	if a.Cert.ACMEStorageDir != want || b.Cert.ACMEStorageDir != want {
		t.Fatalf("共享 ACME 目录错误: %q, %q", a.Cert.ACMEStorageDir, b.Cert.ACMEStorageDir)
	}
	base.Cert.ACMEStorageDir = filepath.Join(base.Kernel.ConfigDir, "custom")
	if got := base.ExpandMachineNode(3, "trojan").Cert.ACMEStorageDir; got != base.Cert.ACMEStorageDir {
		t.Fatalf("自定义 ACME 目录丢失: %q", got)
	}
}
