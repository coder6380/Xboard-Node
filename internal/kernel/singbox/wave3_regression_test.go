package singbox

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/cedar2025/xboard-node/internal/config"
	"github.com/cedar2025/xboard-node/internal/kernel"
	"github.com/cedar2025/xboard-node/internal/model"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singJSON "github.com/sagernet/sing/common/json"
	"golang.org/x/time/rate"
)

func TestRateLimitedWriterWritesFullBufferExceedingBurst(t *testing.T) {
	var dst bytes.Buffer
	// Burst=64 bytes, payload=200 bytes (> Burst): must write all 200 bytes per io.Writer contract.
	w := &RateLimitedWriter{
		writer:  &dst,
		limiter: rate.NewLimiter(1_000_000, 64),
		ctx:     context.Background(),
	}
	payload := bytes.Repeat([]byte("a"), 200)
	n, err := w.Write(payload)
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if n != len(payload) || dst.Len() != len(payload) {
		t.Fatalf("Write() = %d (dst=%d), want %d", n, dst.Len(), len(payload))
	}
}

func TestSingboxCustomRouteRuleSetORSplitAndSourceGeoIP(t *testing.T) {
	cfg := buildConfig(config.KernelConfig{}, &model.NodeSpec{
		Protocol:   "vless",
		ServerPort: 443,
		CustomRouteRules: []model.CustomRouteRule{
			{
				Match: model.RouteMatch{
					Domains:     []string{"geosite:cn", "example.com"},
					Ports:       []string{"80 - 443"},
					SourceCIDRs: []string{"geoip:private"},
				},
				Action: model.RouteAction{Type: " DIRECT "},
			},
		},
	}, []model.UserSpec{{ID: 1, UUID: "u1"}}, kernel.TLSCert{})

	route := cfg["route"].(M)
	rules := route["rules"].([]M)
	if len(rules) < 2 {
		t.Fatalf("expected literal domain and geosite rule_set split into separate OR rules, got %#v", rules)
	}
	// Verify sing-box's own native JSON parser accepts the generated config
	// (including the logical AND rule combining dest geosite + source geoip).
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if _, err := singJSON.UnmarshalExtendedContext[option.Options](include.Context(context.Background()), raw); err != nil {
		t.Fatalf("sing-box failed to parse generated config: %v\nconfig=%s", err, string(raw))
	}

	foundLiteral := false
	foundLogicalRuleSet := false
	for _, r := range rules {
		if r["type"] == "logical" {
			if r["outbound"] != "direct" || r["mode"] != "and" {
				t.Fatalf("unexpected logical rule: %#v", r)
			}
			foundLogicalRuleSet = true
			continue
		}
		if _, hasPortRange := r["port_range"]; !hasPortRange {
			continue
		}
		if r["outbound"] != "direct" {
			t.Fatalf("expected normalized outbound=direct, got %v (rule=%#v)", r["outbound"], r)
		}
		if prs, ok := r["port_range"].([]string); !ok || len(prs) != 1 || prs[0] != "80:443" {
			t.Fatalf("expected normalized port_range [80:443], got %#v", r["port_range"])
		}
		if _, hasDomain := r["domain"]; hasDomain {
			foundLiteral = true
		}
	}
	if !foundLiteral || !foundLogicalRuleSet {
		t.Fatalf("missing split rules: literal=%v logicalRuleSet=%v rules=%#v", foundLiteral, foundLogicalRuleSet, rules)
	}
}

func TestSingboxNaiveWithoutTLSSetsTCPNetworkAndIPv6Reality(t *testing.T) {
	inb := buildInbound(&model.NodeSpec{Protocol: "naive", ServerPort: 8080}, []model.UserSpec{{ID: 1, UUID: "u1"}}, kernel.TLSCert{})
	if inb["network"] != "tcp" {
		t.Fatalf("naive without TLS network = %v, want tcp", inb["network"])
	}

	tls := buildRealityConfig(&model.NodeSpec{
		TLS: 2,
		TLSSettings: map[string]any{
			"dest": "[2001:db8::1]:8443",
		},
	})
	hs := tls["reality"].(M)["handshake"].(M)
	if hs["server"] != "2001:db8::1" || hs["server_port"] != 8443 {
		t.Fatalf("IPv6 reality handshake = %#v, want server=2001:db8::1 port=8443", hs)
	}
}
