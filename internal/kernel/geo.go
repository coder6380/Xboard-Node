package kernel

import (
	"strings"

	"github.com/cedar2025/xboard-node/internal/model"
)

// NeedsGeoIP returns true when any panel route rule contains a "geoip:" match entry.
func NeedsGeoIP(routes []model.RouteRule) bool {
	for _, r := range routes {
		for _, m := range r.Match {
			if strings.HasPrefix(m, "geoip:") {
				return true
			}
		}
	}
	return false
}

// NeedsXrayGeoData 检查 Xray 原生路由规则中的 Geo 数据库引用。
// 参数 rules 是最终生成的路由规则；返回值依次表示是否需要 geoip 和 geosite 数据库。
func NeedsXrayGeoData(rules []map[string]any) (needIP, needSite bool) {
	for _, rule := range rules {
		for _, field := range []string{"ip", "source"} {
			if hasGeoPrefix(rule[field], "geoip:") {
				needIP = true
			}
		}
		if hasGeoPrefix(rule["domain"], "geosite:") {
			needSite = true
		}
	}
	return needIP, needSite
}

// hasGeoPrefix 检查单个路由字段的字符串或字符串列表是否引用指定数据库。
func hasGeoPrefix(value any, prefix string) bool {
	switch values := value.(type) {
	case string:
		return strings.HasPrefix(strings.TrimSpace(values), prefix)
	case []string:
		for _, item := range values {
			if strings.HasPrefix(strings.TrimSpace(item), prefix) {
				return true
			}
		}
	case []any:
		for _, item := range values {
			if text, ok := item.(string); ok && strings.HasPrefix(strings.TrimSpace(text), prefix) {
				return true
			}
		}
	}
	return false
}

// NeedsGeoSite returns true when any panel route rule contains a "geosite:" match entry.
func NeedsGeoSite(routes []model.RouteRule) bool {
	for _, r := range routes {
		for _, m := range r.Match {
			if strings.HasPrefix(m, "geosite:") {
				return true
			}
		}
	}
	return false
}
