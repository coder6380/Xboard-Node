package kernel

import "testing"

// TestNeedsXrayGeoData 验证只识别路由匹配字段中的 Geo 引用。
func TestNeedsXrayGeoData(t *testing.T) {
	tests := []struct {
		name     string
		rules    []map[string]any
		wantIP   bool
		wantSite bool
	}{
		{name: "空规则"},
		{name: "目标地址", rules: []map[string]any{{"ip": []string{"geoip:cn"}}}, wantIP: true},
		{name: "来源地址", rules: []map[string]any{{"source": []any{"geoip:private"}}}, wantIP: true},
		{name: "域名", rules: []map[string]any{{"domain": "geosite:cn"}}, wantSite: true},
		{name: "混合引用", rules: []map[string]any{{"ip": []any{"  geoip:cn"}, "domain": []string{"geosite:cn"}}}, wantIP: true, wantSite: true},
		{name: "非匹配字段不触发", rules: []map[string]any{{"outboundTag": "geoip:cn", "ip": []string{"192.0.2.0/24"}, "domain": []any{"full:geoip:cn", 42}}}},
		{name: "字段类型不匹配", rules: []map[string]any{{"ip": map[string]any{"nested": "geoip:cn"}, "domain": 12}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIP, gotSite := NeedsXrayGeoData(tt.rules)
			if gotIP != tt.wantIP || gotSite != tt.wantSite {
				t.Errorf("需求 = (%v, %v), 期望 (%v, %v)", gotIP, gotSite, tt.wantIP, tt.wantSite)
			}
		})
	}
}
