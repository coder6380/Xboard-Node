package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cedar2025/xboard-node/internal/config"
	panelapi "github.com/cedar2025/xboard-node/internal/panel"
)

func TestPanelControlPlaneInitialRejectsInvalidCustomOutbounds(t *testing.T) {
	server := newPanelTestServer(`{"protocol":"shadowsocks","server_port":8388,"custom_outbounds":[{"tag":"proxy","protocol":"socks","proxy_tag":"missing","settings":{"server":"2.2.2.2","server_port":1080}}]}`)
	defer server.Close()

	cp := NewPanelControlPlane(config.PanelConfig{URL: server.URL, Token: "token", NodeID: 1}, config.WSConfig{}, config.KernelConfig{Type: "singbox"})
	_, err := cp.Initial(context.Background(), nil, nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `initial config normalize: validate custom outbounds: custom_outbounds[0].proxy_tag references unknown outbound "missing"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPanelControlPlanePollRejectsInvalidCustomOutbounds(t *testing.T) {
	server := newPanelTestServer(`{"protocol":"shadowsocks","server_port":8388,"custom_outbounds":[{"tag":"proxy","protocol":"socks","proxy_tag":"missing","settings":{"server":"2.2.2.2","server_port":1080}}]}`)
	defer server.Close()

	cp := NewPanelControlPlane(config.PanelConfig{URL: server.URL, Token: "token", NodeID: 1}, config.WSConfig{}, config.KernelConfig{Type: "singbox"})
	_, err := cp.Poll(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `poll config normalize: validate custom outbounds: custom_outbounds[0].proxy_tag references unknown outbound "missing"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestPollRetriesDiscardedSnapshots 验证轮询失败后不会因缓存 ETag 而丢失快照。
func TestPollRetriesDiscardedSnapshots(t *testing.T) {
	for _, mode := range []string{"panel", "machine"} {
		for _, failure := range []string{"users", "validation", "canceled"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				configRequests, userRequests := 0, 0
				mux := http.NewServeMux()
				mux.HandleFunc("/api/v1/server/UniProxy/config", func(w http.ResponseWriter, r *http.Request) {
					configRequests++
					if r.Header.Get("If-None-Match") == `"config"` {
						w.WriteHeader(http.StatusNotModified)
						return
					}
					w.Header().Set("ETag", `"config"`)
					if failure == "validation" && configRequests == 1 {
						_, _ = w.Write([]byte(`{"protocol":"shadowsocks","server_port":8388,"custom_outbounds":[{"tag":"proxy","protocol":"socks","proxy_tag":"missing","settings":{"server":"2.2.2.2","server_port":1080}}]}`))
						return
					}
					_, _ = w.Write([]byte(`{"protocol":"shadowsocks","server_port":8388}`))
				})
				mux.HandleFunc("/api/v1/server/UniProxy/user", func(w http.ResponseWriter, r *http.Request) {
					userRequests++
					if failure == "users" && userRequests == 1 {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					if r.Header.Get("If-None-Match") == `"users"` {
						w.WriteHeader(http.StatusNotModified)
						return
					}
					w.Header().Set("ETag", `"users"`)
					_, _ = w.Write([]byte(`{"users":[{"id":1,"uuid":"11111111-1111-1111-1111-111111111111"}]}`))
				})
				server := httptest.NewServer(mux)
				defer server.Close()
				cfg := config.PanelConfig{URL: server.URL, Token: "token", NodeID: 1}
				var poll func(context.Context) (Snapshot, error)
				if mode == "machine" {
					poll = NewMachinePanelControlPlane(panelapi.NewClient(cfg), config.KernelConfig{Type: "singbox"}, nil, nil).Poll
				} else {
					poll = NewPanelControlPlane(cfg, config.WSConfig{}, config.KernelConfig{Type: "singbox"}).Poll
				}
				ctx := context.Background()
				if failure == "canceled" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
				if _, err := poll(ctx); err == nil {
					t.Fatal("expected first poll to fail")
				}
				snapshot, err := poll(context.Background())
				if err != nil {
					t.Fatalf("retry failed: %v", err)
				}
				if snapshot.Config == nil || len(snapshot.Users) != 1 {
					t.Fatalf("retry lost snapshot: config=%v users=%v", snapshot.Config, snapshot.Users)
				}
			})
		}
	}
}

func TestTranslateWSEventRejectsInvalidCustomOutbounds(t *testing.T) {
	_, err := TranslateWSEvent(panelapi.WSEvent{
		Type: panelapi.WSEventSyncConfig,
		Config: &panelapi.NodeConfig{
			Protocol:   "shadowsocks",
			ServerPort: 8388,
			CustomOutbounds: []panelapi.OutboundConfig{
				{Tag: "proxy", Protocol: "socks", ProxyTag: "missing", Settings: map[string]any{"server": "2.2.2.2", "server_port": 1080}},
			},
		},
	}, config.KernelConfig{Type: "singbox"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `translate node config: validate custom outbounds: custom_outbounds[0].proxy_tag references unknown outbound "missing"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func newPanelTestServer(configBody string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/server/handshake", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"websocket":{"enabled":false},"settings":{"push_interval":60,"pull_interval":60}}`))
	})
	mux.HandleFunc("/api/v1/server/UniProxy/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(configBody))
	})
	mux.HandleFunc("/api/v1/server/UniProxy/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"users":[{"id":1,"uuid":"11111111-1111-1111-1111-111111111111"}]}`))
	})
	return httptest.NewServer(mux)
}

func TestTranslateWSEventRejectsUnsupportedProtocolForKernel(t *testing.T) {
	_, err := TranslateWSEvent(panelapi.WSEvent{
		Type: panelapi.WSEventSyncConfig,
		Config: &panelapi.NodeConfig{
			Protocol:   "shadowsocks",
			ServerPort: 8388,
			CustomOutbounds: []panelapi.OutboundConfig{
				{Tag: "hy2", Protocol: "hysteria2", Settings: map[string]any{"server": "2.2.2.2", "server_port": 8443}},
			},
		},
	}, config.KernelConfig{Type: "xray"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `translate node config: validate custom outbounds: custom_outbounds[0].protocol "hysteria2" is not supported by kernel "xray"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}
