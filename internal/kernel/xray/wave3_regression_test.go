package xray

import (
	"testing"

	"github.com/cedar2025/xboard-node/internal/config"
	"github.com/cedar2025/xboard-node/internal/kernel"
	"github.com/cedar2025/xboard-node/internal/model"
)

func TestAggregateStatsNilInstanceSafe(t *testing.T) {
	x := New(config.KernelConfig{})
	x.mu.Lock()
	traffic, err := x.aggregateStats()
	x.mu.Unlock()
	if err != nil || traffic != nil {
		t.Fatalf("aggregateStats on nil instance = (%v, %v), want (nil, nil)", traffic, err)
	}
}

func TestToMemoryUserHysteria2(t *testing.T) {
	mu, err := toMemoryUser("hysteria", &model.NodeSpec{Protocol: "hysteria", Version: 2}, model.UserSpec{ID: 7, UUID: "secret-auth"})
	if err != nil {
		t.Fatalf("toMemoryUser(hysteria) error: %v", err)
	}
	if mu.Email != "user@7" || mu.Account == nil {
		t.Fatalf("unexpected hysteria MemoryUser: %+v", mu)
	}
}

func TestSanitizeEmptyArraysDoesNotMutateCaller(t *testing.T) {
	orig := map[string]any{
		"keep":  "val",
		"empty": []any{},
		"nested": map[string]any{
			"innerEmpty": []any{},
			"innerKeep":  123,
		},
	}
	got := sanitizeEmptyArrays(orig)
	if _, exists := got["empty"]; exists {
		t.Fatal("expected empty array stripped from returned map")
	}
	if _, exists := orig["empty"]; !exists {
		t.Fatal("sanitizeEmptyArrays mutated caller map in-place")
	}
	nestedOrig := orig["nested"].(map[string]any)
	if _, exists := nestedOrig["innerEmpty"]; !exists {
		t.Fatal("sanitizeEmptyArrays mutated nested caller map in-place")
	}
}

func TestBuildHysteria2SalamanderAndRealityIPv6(t *testing.T) {
	nc := &model.NodeSpec{
		Protocol:     "hysteria2",
		ServerPort:   8443,
		UpMbps:       100,
		DownMbps:     200,
		Obfs:         "salamander",
		ObfsPassword: "obfs-secret",
		ServerName:   "hy2.example.com",
	}
	cfg := buildConfig(config.KernelConfig{}, nc, []model.UserSpec{{ID: 1, UUID: "u1"}}, kernel.TLSCert{
		CertPEM: []byte("cert"),
		KeyPEM:  []byte("key"),
	})
	inbounds := cfg["inbounds"].([]M)
	if len(inbounds) != 1 || inbounds[0]["protocol"] != "hysteria" {
		t.Fatalf("unexpected hysteria2 inbound: %#v", inbounds)
	}
	ss := inbounds[0]["streamSettings"].(M)
	fm := ss["finalmask"].(M)
	udpMasks := fm["udp"].([]M)
	if len(udpMasks) != 1 || udpMasks[0]["type"] != "salamander" {
		t.Fatalf("expected salamander udp mask, got %#v", fm)
	}

	reality := buildRealitySettings(&model.NodeSpec{
		TLSSettings: map[string]any{
			"dest":        "2001:db8::1",
			"server_port": 8443,
		},
	})
	if reality["dest"] != "[2001:db8::1]:8443" {
		t.Fatalf("IPv6 reality dest = %v, want [2001:db8::1]:8443", reality["dest"])
	}
}

func TestCompileCustomRouteRulePortRangeAndCaseAction(t *testing.T) {
	rules := compileCustomRouteRule(model.CustomRouteRule{
		Match: model.RouteMatch{
			Ports:       []string{"", "80 : 443", "8080"},
			SourcePorts: []string{"1000 - 2000"},
			Networks:    []string{" TCP ", ""},
		},
		Action: model.RouteAction{Type: " DIRECT "},
	})
	if len(rules) != 1 {
		t.Fatalf("expected 1 compiled rule, got %d", len(rules))
	}
	if rules[0]["outboundTag"] != "direct" {
		t.Fatalf("outboundTag = %v, want direct", rules[0]["outboundTag"])
	}
	if rules[0]["port"] != "8080,80-443" {
		t.Fatalf("port = %v, want 8080,80-443", rules[0]["port"])
	}
	if rules[0]["sourcePort"] != "1000-2000" {
		t.Fatalf("sourcePort = %v, want 1000-2000", rules[0]["sourcePort"])
	}
	if rules[0]["network"] != "tcp" {
		t.Fatalf("network = %v, want tcp", rules[0]["network"])
	}
}
