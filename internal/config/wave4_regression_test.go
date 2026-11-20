package config

import (
	"path/filepath"
	"testing"
)

func TestExpandNodesIsolatesConfigDirAndCertDirAndMergesCert(t *testing.T) {
	base := &Config{
		Panel:  PanelConfig{URL: "https://panel.example.com", Token: "tok"},
		Kernel: KernelConfig{Type: "singbox", ConfigDir: "/etc/xboard-node"},
		Cert: CertConfig{
			CertMode:    "dns",
			Email:       "ops@example.com",
			DNSProvider: "cloudflare",
			DNSEnv:      map[string]string{"CLOUDFLARE_API_TOKEN": "tok"},
			CertDir:     "/etc/xboard-node/certs",
		},
		Nodes: []NodeEntry{
			{
				NodeID: 1,
				Kernel: &KernelOverride{LogLevel: "debug"}, // no ConfigDir set
				Cert:   &CertConfig{Domain: "n1.example.com"},
			},
			{
				NodeID: 2,
				Cert:   &CertConfig{Domain: "n2.example.com"},
			},
		},
	}

	expanded := base.ExpandNodes()
	if len(expanded) != 2 {
		t.Fatalf("len(expanded) = %d, want 2", len(expanded))
	}
	if expanded[0].Kernel.ConfigDir == expanded[1].Kernel.ConfigDir {
		t.Fatalf("ConfigDir not isolated: %q vs %q", expanded[0].Kernel.ConfigDir, expanded[1].Kernel.ConfigDir)
	}
	if expanded[0].Cert.CertDir == expanded[1].Cert.CertDir {
		t.Fatalf("CertDir not isolated: %q vs %q", expanded[0].Cert.CertDir, expanded[1].Cert.CertDir)
	}
	if expanded[0].Cert.Email != "ops@example.com" || expanded[0].Cert.DNSProvider != "cloudflare" {
		t.Fatalf("per-node Cert override wiped inherited parent fields: %+v", expanded[0].Cert)
	}
	wantShared := filepath.Join("/etc/xboard-node", "acme-shared")
	if expanded[0].Cert.ACMEStorageDir != wantShared {
		t.Fatalf("ACMEStorageDir = %q, want %q", expanded[0].Cert.ACMEStorageDir, wantShared)
	}
}

func TestValidateRejectsNegativeIntervals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"track_interval", func(c *Config) { c.Node.TrackInterval = -1 }},
		{"device_report_interval", func(c *Config) { c.Node.DeviceReportInterval = -1 }},
		{"ws.status_interval", func(c *Config) { c.WS.StatusInterval = -1 }},
		{"ws.discovery_interval", func(c *Config) { c.WS.DiscoveryInterval = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{
				Panel:  PanelConfig{URL: "https://p.example.com", Token: "t", NodeID: 1},
				Kernel: KernelConfig{Type: "singbox"},
			}
			c.setDefaultsFrom("/tmp")
			tc.mutate(c)
			if err := c.validate(); err == nil {
				t.Fatalf("expected validation error for negative %s", tc.name)
			}
		})
	}
}

func TestMultipleStandaloneInstancesUniqueIDsAndLayout(t *testing.T) {
	a := &Config{
		Kernel: KernelConfig{Type: "singbox", ConfigDir: "/tmp/s1"},
		Standalone: &StandaloneConfig{
			Enabled: true,
			Node:    StandaloneNodeConfig{Protocol: "vless", ServerPort: 8443},
			Users:   []StandaloneUser{{ID: 1, UUID: "u1"}},
		},
	}
	b := &Config{
		Kernel: KernelConfig{Type: "singbox", ConfigDir: "/tmp/s2"},
		Standalone: &StandaloneConfig{
			Enabled: true,
			Node:    StandaloneNodeConfig{Protocol: "trojan", ServerPort: 9443},
			Users:   []StandaloneUser{{ID: 1, UUID: "u1"}},
		},
	}
	idA, err := a.AutoInstanceID()
	if err != nil {
		t.Fatalf("idA: %v", err)
	}
	idB, err := b.AutoInstanceID()
	if err != nil {
		t.Fatalf("idB: %v", err)
	}
	if idA == idB {
		t.Fatalf("standalone AutoInstanceID collided: %q", idA)
	}
	a.InstanceID, b.InstanceID = idA, idB
	if err := ValidateStartupLayout([]*Config{a, b}); err != nil {
		t.Fatalf("ValidateStartupLayout failed for distinct standalone instances: %v", err)
	}
}
