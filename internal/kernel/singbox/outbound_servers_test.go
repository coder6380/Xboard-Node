package singbox

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cedar2025/xboard-node/internal/model"
)

// TestOutboundServersConversion 验证单服务器转换及无法无损转换时的拒绝行为。
func TestOutboundServersConversion(t *testing.T) {
	for _, protocol := range []string{"socks", "shadowsocks"} {
		t.Run(protocol, func(t *testing.T) {
			m := outboundConfigToSingbox(model.OutboundConfig{Protocol: protocol, Settings: map[string]any{
				"servers": []any{map[string]any{"address": "127.0.0.1", "port": "1080", "password": "secret"}},
				"server":  "example.com",
			}})
			if _, ok := m["servers"]; ok || m["server"] != "example.com" || m["server_port"] != 1080 || m["password"] != "secret" {
				t.Fatalf("转换结果不正确: %#v", m)
			}
		})
	}
	t.Run("users", func(t *testing.T) {
		m := outboundConfigToSingbox(model.OutboundConfig{Protocol: "socks", Settings: map[string]any{
			"servers": []any{map[string]any{"address": "host", "port": 1080, "users": []any{map[string]any{"user": "alice", "pass": "secret"}}}},
		}})
		if m["username"] != "alice" || m["password"] != "secret" {
			t.Fatalf("用户信息转换失败: %#v", m)
		}
	})
	for name, servers := range map[string]any{
		"multiple":       []any{map[string]any{"address": "a"}, map[string]any{"address": "b"}},
		"empty":          []any{},
		"unknown":        []any{map[string]any{"address": "a", "extra": true}},
		"multiple users": []any{map[string]any{"users": []any{map[string]any{"user": "a"}, map[string]any{"user": "b"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			m := outboundConfigToSingbox(model.OutboundConfig{Protocol: "socks", Settings: map[string]any{"servers": servers}})
			data, err := json.Marshal(m)
			if err != nil || !strings.Contains(string(data), `"servers"`) {
				t.Fatalf("不支持的配置必须保留以便解析时报错: %s, %v", data, err)
			}
		})
	}
}
