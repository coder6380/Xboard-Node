package singbox

import (
	"encoding/base64"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cedar2025/xboard-node/internal/config"
	"github.com/cedar2025/xboard-node/internal/kernel"
	"github.com/cedar2025/xboard-node/internal/model"
	"github.com/cedar2025/xboard-node/internal/nlog"
	"github.com/go-viper/mapstructure/v2"
)

// M is a shorthand for building JSON-like maps
type M = map[string]interface{}

func buildConfig(kcfg config.KernelConfig, nc *model.NodeSpec, users []model.UserSpec, tc kernel.TLSCert) M {
	var outbounds []M
	var endpoints []M
	tags := make(map[string]bool)

	customCfg, err := kernel.LoadCustomConfig(kcfg.CustomConfig)
	if err != nil {
		nlog.Core().Error("failed to load custom sing-box config", "error", err)
	}
	for tag := range kernel.CustomConfigOutboundTags(customCfg) {
		tags[tag] = true
	}

	// Panel-defined custom outbounds (structured, converted to sing-box native).
	// WireGuard was removed from sing-box outbounds in 1.13.0 and must be emitted
	// under top-level "endpoints".
	for _, co := range nc.CustomOutbounds {
		converted := outboundConfigToSingbox(co)
		if tag := strings.TrimSpace(co.Tag); tag != "" {
			tags[tag] = true
		}
		if strings.EqualFold(strings.TrimSpace(co.Protocol), "wireguard") {
			endpoints = append(endpoints, converted)
		} else {
			outbounds = append(outbounds, converted)
		}
	}

	// Static outbounds from local config file
	for _, co := range kcfg.CustomOutbound {
		if tag, _ := co["tag"].(string); strings.TrimSpace(tag) != "" {
			tags[strings.TrimSpace(tag)] = true
		}
		typ, _ := co["type"].(string)
		if strings.EqualFold(strings.TrimSpace(typ), "wireguard") || strings.EqualFold(strings.TrimSpace(typ), "tailscale") {
			endpoints = append(endpoints, M(co))
		} else {
			outbounds = append(outbounds, M(co))
		}
	}

	// Add default outbounds only if exact lowercase "direct"/"block" is not already defined
	if !tags["direct"] {
		outbounds = append([]M{{"type": "direct", "tag": "direct"}}, outbounds...)
	}
	if !tags["block"] {
		outbounds = append(outbounds, M{"type": "block", "tag": "block"})
	}

	cfg := M{
		"log": M{
			"level":     kcfg.LogLevel,
			"timestamp": true,
		},
		"outbounds": outbounds,
	}
	if len(endpoints) > 0 {
		cfg["endpoints"] = endpoints
	}

	inbound := buildInbound(nc, users, tc)
	if inbound != nil {
		cfg["inbounds"] = []M{inbound}
	}

	// Merge panel routes and static config routes
	cfg["route"] = buildRoutes(nc.Routes, nc.CustomRouteRules, mergeRouteList(nc.CustomRoutes, kcfg.CustomRoute))

	// Automatically enable rule_set caching (cache_file) when panel routes
	// reference geoip:/geosite: entries so that the downloaded .srs rule_set
	// files survive across process restarts.
	if kernel.NeedsGeoIP(nc.Routes) || kernel.NeedsGeoSite(nc.Routes) || kernel.NeedsGeoIPRules(nc.CustomRouteRules) || kernel.NeedsGeoSiteRules(nc.CustomRouteRules) {
		cfg["experimental"] = M{
			"cache_file": M{
				"enabled": true,
				"path":    filepath.Join(kcfg.ConfigDir, "cache.db"),
			},
		}
	}

	mergeCustomSingboxMap(cfg, customCfg)
	return cfg
}

// outboundConfigToSingbox converts a structured OutboundConfig (from the panel)
// into a sing-box outbound object. sing-box uses a flat layout where all
// protocol-specific fields sit at the top level alongside "type" and "tag".
func outboundConfigToSingbox(oc model.OutboundConfig) M {
	proto := strings.ToLower(strings.TrimSpace(oc.Protocol))
	m := M{
		"type": proto,
		"tag":  strings.TrimSpace(oc.Tag),
	}

	// Transform common protocol keys to sing-box native format
	// WireGuard: secret_key -> private_key; peers: endpoint -> address+port
	if proto == "wireguard" {
		if sk, ok := oc.Settings["secret_key"]; ok {
			m["private_key"] = sk
		}
		if peers, ok := oc.Settings["peers"].([]any); ok {
			var wgPeers []M
			for _, p := range peers {
				if peerMap, ok := p.(map[string]any); ok {
					newPeer := M{}
					for k, v := range peerMap {
						if k == "endpoint" {
							if ep, ok := v.(string); ok {
								host, portStr, err := net.SplitHostPort(ep)
								if err == nil {
									newPeer["address"] = host
									port, _ := strconv.Atoi(portStr)
									newPeer["port"] = port
								} else {
									newPeer["address"] = ep
								}
							}
						} else {
							newPeer[k] = v
						}
					}
					wgPeers = append(wgPeers, newPeer)
				}
			}
			m["peers"] = wgPeers
		}

		// Copy any other top-level settings not handled above
		for k, v := range oc.Settings {
			if k != "secret_key" && k != "peers" {
				m[k] = v
			}
		}
	} else {
		for k, v := range oc.Settings {
			m[k] = v
		}
		if protocol := strings.ToLower(strings.TrimSpace(oc.Protocol)); protocol == "socks" || protocol == "shadowsocks" {
			flattenOutboundServers(m)
		}
	}

	if oc.ProxyTag != "" {
		m["detour"] = oc.ProxyTag
	}
	return m
}

// flattenOutboundServers 将可无损转换的单服务器配置转换为 sing-box 原生字段。
// 无法转换时保留 servers，让 sing-box 解析器明确报错，避免静默丢弃配置。
func flattenOutboundServers(m M) {
	raw, exists := m["servers"]
	if !exists {
		return
	}
	var server map[string]any
	switch list := raw.(type) {
	case []any:
		if len(list) != 1 {
			return
		}
		server, _ = list[0].(map[string]any)
	case []map[string]any:
		if len(list) != 1 {
			return
		}
		server = list[0]
	}
	if server == nil {
		return
	}

	converted := M{}
	for key, value := range server {
		target := key
		switch key {
		case "address":
			target = "server"
		case "port":
			target = "server_port"
		case "server", "server_port", "method", "password", "plugin", "plugin_opts", "network", "version", "username":
		case "users":
			users, ok := value.([]any)
			if !ok || len(users) != 1 {
				return
			}
			user, ok := users[0].(map[string]any)
			if !ok {
				return
			}
			for userKey, userValue := range user {
				var target string
				switch userKey {
				case "user", "username":
					target = "username"
				case "pass", "password":
					target = "password"
				default:
					return
				}
				if _, exists := converted[target]; exists {
					return
				}
				converted[target] = userValue
			}
			continue
		default:
			return
		}
		if target == "server_port" {
			if port, ok := value.(string); ok {
				parsed, err := strconv.Atoi(strings.TrimSpace(port))
				if err != nil || parsed < 1 || parsed > 65535 {
					return
				}
				value = parsed
			}
		}
		if _, exists := converted[target]; exists {
			return
		}
		converted[target] = value
	}
	delete(m, "servers")
	for key, value := range converted {
		// 原生扁平字段优先，兼容现有面板配置。
		if _, exists := m[key]; !exists {
			m[key] = value
		}
	}
}

func mergeRouteList(a, b []map[string]any) []map[string]any {
	res := make([]map[string]any, 0, len(a)+len(b))
	res = append(res, a...)
	res = append(res, b...)
	return res
}

func buildRoutes(panelRoutes []model.RouteRule, customRules []model.CustomRouteRule, custom []map[string]any) M {
	var rules []M
	ruleSetTags := map[string]struct{}{}
	var ruleSets []M

	addRuleSet := func(kind, code string) string {
		code = strings.ToLower(strings.TrimSpace(code))
		if code == "" {
			return ""
		}
		tag := kind + "-" + code
		if _, exists := ruleSetTags[tag]; !exists {
			ruleSetTags[tag] = struct{}{}
			repo := "sing-geoip"
			if kind == "geosite" {
				repo = "sing-geosite"
			}
			ruleSets = append(ruleSets, M{
				"tag":    tag,
				"type":   "remote",
				"format": "binary",
				"url":    fmt.Sprintf("https://raw.githubusercontent.com/SagerNet/%s/rule-set/%s.srs", repo, tag),
			})
		}
		return tag
	}

	// Structured custom routes now take the highest priority for panel-managed overrides.
	for _, rule := range customRules {
		if rule.Disabled {
			continue
		}
		rules = append(rules, compileCustomRouteRuleWithRuleSets(rule, addRuleSet)...)
	}

	// Raw custom routes remain the escape hatch, but no longer outrank structured rules.
	for _, cr := range custom {
		rules = append(rules, M(cr))
	}

	// Standard blocks for private IPv4 and IPv6 ranges to prevent SSRF.
	rules = append(rules,
		M{
			"outbound": "block",
			"ip_cidr": []string{
				"10.0.0.0/8",
				"100.64.0.0/10",
				"127.0.0.0/8",
				"169.254.0.0/16",
				"172.16.0.0/12",
				"192.0.0.0/24",
				"192.168.0.0/16",
				"198.18.0.0/15",
			},
		},
		M{
			"outbound": "block",
			"ip_cidr": []string{
				"fc00::/7",
				"fe80::/10",
				"::1/128",
			},
		},
	)

	for _, pr := range panelRoutes {
		rules = append(rules, compilePanelRouteRule(pr, addRuleSet)...)
	}

	route := M{
		"final": "direct",
		"rules": rules,
	}
	if len(ruleSets) > 0 {
		route["rule_set"] = ruleSets
	}
	return route
}

func compilePanelRouteRule(pr model.RouteRule, addRuleSet func(kind, code string) string) []M {
	if len(pr.Match) == 0 {
		return nil
	}

	var domains, cidrs, ruleSets []string
	for _, item := range pr.Match {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.HasPrefix(item, "geoip:") {
			if tag := addRuleSet("geoip", strings.TrimPrefix(item, "geoip:")); tag != "" {
				ruleSets = append(ruleSets, tag)
			}
			continue
		}
		if strings.HasPrefix(item, "geosite:") {
			if tag := addRuleSet("geosite", strings.TrimPrefix(item, "geosite:")); tag != "" {
				ruleSets = append(ruleSets, tag)
			}
			continue
		}
		item = strings.TrimPrefix(item, "*.")
		if strings.Contains(item, "/") {
			cidrs = append(cidrs, item)
			continue
		}
		if ip := net.ParseIP(item); ip != nil {
			if ip.To4() != nil {
				cidrs = append(cidrs, item+"/32")
			} else {
				cidrs = append(cidrs, item+"/128")
			}
			continue
		}
		domains = append(domains, item)
	}

	applyAction := func(rule M) M {
		switch pr.Action {
		case "direct":
			rule["outbound"] = "direct"
		case "dns":
			if pr.ActionValue != "" {
				rule["outbound"] = pr.ActionValue
			} else {
				rule["action"] = "hijack-dns"
			}
		case "proxy":
			if pr.ActionValue != "" {
				rule["outbound"] = pr.ActionValue
			} else {
				rule["outbound"] = "block"
			}
		default:
			rule["outbound"] = "block"
		}
		return rule
	}

	var compiled []M
	if len(domains) > 0 {
		compiled = append(compiled, applyAction(M{
			"domain_suffix": copyStrings(domains),
		}))
	}
	if len(cidrs) > 0 {
		compiled = append(compiled, applyAction(M{
			"ip_cidr": copyStrings(cidrs),
		}))
	}
	if len(ruleSets) > 0 {
		compiled = append(compiled, applyAction(M{
			"rule_set": copyStrings(ruleSets),
		}))
	}
	return compiled
}

func compileCustomRouteRule(rule model.CustomRouteRule) []M {
	return compileCustomRouteRuleWithRuleSets(rule, nil)
}

func compileCustomRouteRuleWithRuleSets(rule model.CustomRouteRule, addRuleSet func(kind, code string) string) []M {
	if rule.Disabled {
		return nil
	}

	outbound := singboxOutboundForAction(rule.Action)
	entry := M{"outbound": outbound}
	var domains, suffixes, cidrs, destRuleSets []string
	for _, raw := range rule.Match.Domains {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if addRuleSet != nil && strings.HasPrefix(v, "geosite:") {
			if tag := addRuleSet("geosite", strings.TrimPrefix(v, "geosite:")); tag != "" {
				destRuleSets = append(destRuleSets, tag)
			}
			continue
		}
		domains = append(domains, v)
	}
	for _, raw := range rule.Match.DomainSuffixes {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if addRuleSet != nil && strings.HasPrefix(v, "geosite:") {
			if tag := addRuleSet("geosite", strings.TrimPrefix(v, "geosite:")); tag != "" {
				destRuleSets = append(destRuleSets, tag)
			}
			continue
		}
		suffixes = append(suffixes, v)
	}
	for _, raw := range rule.Match.IPCIDRs {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if addRuleSet != nil && strings.HasPrefix(v, "geoip:") {
			if tag := addRuleSet("geoip", strings.TrimPrefix(v, "geoip:")); tag != "" {
				destRuleSets = append(destRuleSets, tag)
			}
			continue
		}
		if ip := net.ParseIP(v); ip != nil {
			if ip.To4() != nil {
				v += "/32"
			} else {
				v += "/128"
			}
		}
		cidrs = append(cidrs, v)
	}

	var sourceCIDRs, sourceRuleSets []string
	for _, raw := range rule.Match.SourceCIDRs {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if addRuleSet != nil && strings.HasPrefix(v, "geoip:") {
			if tag := addRuleSet("geoip", strings.TrimPrefix(v, "geoip:")); tag != "" {
				sourceRuleSets = append(sourceRuleSets, tag)
			}
			continue
		}
		if ip := net.ParseIP(v); ip != nil {
			if ip.To4() != nil {
				v += "/32"
			} else {
				v += "/128"
			}
		}
		sourceCIDRs = append(sourceCIDRs, v)
	}

	ports, portRanges := splitPorts(rule.Match.Ports)
	sourcePorts, sourcePortRanges := splitPorts(rule.Match.SourcePorts)
	networks := kernel.NormalizeNetworks(rule.Match.Networks)

	cloneBase := func(includeSourceCIDRs bool) (M, bool) {
		e := M{}
		for k, v := range entry {
			e[k] = v
		}
		hasShared := false
		if len(ports) > 0 {
			e["port"] = append([]int(nil), ports...)
			hasShared = true
		}
		if len(portRanges) > 0 {
			e["port_range"] = copyStrings(portRanges)
			hasShared = true
		}
		if len(networks) > 0 {
			e["network"] = copyStrings(networks)
			hasShared = true
		}
		if includeSourceCIDRs && len(sourceCIDRs) > 0 {
			e["source_ip_cidr"] = copyStrings(sourceCIDRs)
			hasShared = true
		}
		if len(sourcePorts) > 0 {
			e["source_port"] = append([]int(nil), sourcePorts...)
			hasShared = true
		}
		if len(sourcePortRanges) > 0 {
			e["source_port_range"] = copyStrings(sourcePortRanges)
			hasShared = true
		}
		return e, hasShared
	}

	hasLiteralDest := len(domains) > 0 || len(suffixes) > 0 || len(cidrs) > 0
	var compiled []M

	// Sing-box ANDs rule_set with domain/domain_suffix/ip_cidr inside a single
	// DefaultRule. Emit separate OR rules when both literal destination matchers
	// and destination rule_set entries are present.
	emitForDest := func(includeSourceCIDRs bool, extraSourceRuleSets []string) {
		attachLiteralDest := func(e M) {
			if len(domains) > 0 {
				e["domain"] = copyStrings(domains)
			}
			if len(suffixes) > 0 {
				e["domain_suffix"] = copyStrings(suffixes)
			}
			if len(cidrs) > 0 {
				e["ip_cidr"] = copyStrings(cidrs)
			}
		}
		if hasLiteralDest {
			e, _ := cloneBase(includeSourceCIDRs)
			attachLiteralDest(e)
			if len(extraSourceRuleSets) > 0 {
				e["rule_set"] = copyStrings(extraSourceRuleSets)
				e["rule_set_ip_cidr_match_source"] = true
			}
			compiled = append(compiled, e)
		}
		if len(destRuleSets) > 0 {
			if len(extraSourceRuleSets) > 0 {
				// Combine destination rule_set AND source geoip rule_set via a
				// sing-box logical AND rule so both constraints must hold simultaneously.
				destSub, _ := cloneBase(false)
				delete(destSub, "outbound")
				delete(destSub, "action")
				destSub["rule_set"] = copyStrings(destRuleSets)
				srcSub := M{
					"rule_set":                      copyStrings(extraSourceRuleSets),
					"rule_set_ip_cidr_match_source": true,
				}
				logical := M{
					"type":  "logical",
					"mode":  "and",
					"rules": []M{destSub, srcSub},
				}
				for k, v := range entry {
					logical[k] = v
				}
				compiled = append(compiled, logical)
			} else {
				e, _ := cloneBase(includeSourceCIDRs)
				e["rule_set"] = copyStrings(destRuleSets)
				compiled = append(compiled, e)
			}
		}
		if !hasLiteralDest && len(destRuleSets) == 0 {
			e, hasShared := cloneBase(includeSourceCIDRs)
			if len(extraSourceRuleSets) > 0 {
				e["rule_set"] = copyStrings(extraSourceRuleSets)
				e["rule_set_ip_cidr_match_source"] = true
				hasShared = true
			}
			if hasShared {
				compiled = append(compiled, e)
			}
		}
	}

	if len(sourceRuleSets) > 0 {
		if len(sourceCIDRs) > 0 {
			emitForDest(true, nil)
		}
		emitForDest(false, sourceRuleSets)
	} else {
		emitForDest(true, nil)
	}
	return compiled
}

func singboxOutboundForAction(action model.RouteAction) string {
	switch strings.ToLower(strings.TrimSpace(action.Type)) {
	case "direct":
		return "direct"
	case "route":
		if target := strings.TrimSpace(action.Target); target != "" {
			return target
		}
		return "block"
	default:
		return "block"
	}
}

func splitPorts(values []string) ([]int, []string) {
	return kernel.NormalizePortSpecs(values, ":")
}

func copyStrings(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

func mergeCustomSingbox(cfg M, kcfg config.KernelConfig) {
	custom, err := kernel.LoadCustomConfig(kcfg.CustomConfig)
	if err != nil {
		nlog.Core().Error("failed to load custom sing-box config", "error", err)
		return
	}
	mergeCustomSingboxMap(cfg, custom)
}

func mergeCustomSingboxMap(cfg M, custom map[string]any) {
	if custom == nil {
		return
	}

	// dns — custom replaces
	if v, ok := custom["dns"]; ok {
		cfg["dns"] = v
	}

	// experimental — deep-merge sub-keys so auto-generated cache_file is preserved
	// unless explicitly overridden by custom_config.
	if v, ok := custom["experimental"]; ok {
		if customExp, ok := v.(map[string]any); ok {
			merged := M{}
			if existingExp, ok := cfg["experimental"].(M); ok {
				for k, val := range existingExp {
					merged[k] = val
				}
			}
			for k, val := range customExp {
				merged[k] = val
			}
			cfg["experimental"] = merged
		} else {
			cfg["experimental"] = v
		}
	}

	// outbounds — append custom entries
	if v, ok := custom["outbounds"]; ok {
		if existing, ok := cfg["outbounds"].([]M); ok {
			cfg["outbounds"] = kernel.MergeAppendList(existing, v)
		}
	}

	// endpoints (sing-box 1.11+ wireguard etc.) — append
	if v, ok := custom["endpoints"]; ok {
		if existing, ok := cfg["endpoints"].([]M); ok {
			cfg["endpoints"] = kernel.MergeAppendList(existing, v)
		} else {
			if items := kernel.MergeAppendList(nil, v); len(items) > 0 {
				cfg["endpoints"] = items
			}
		}
	}

	// route — merge sub-fields
	if customRoute, ok := custom["route"]; ok {
		if customRouteMap, ok := customRoute.(map[string]any); ok {
			mergeCustomSingboxRoute(cfg, customRouteMap)
		}
	}
}

func mergeCustomSingboxRoute(cfg M, customRoute map[string]any) {
	route, ok := cfg["route"].(M)
	if !ok {
		route = M{}
		cfg["route"] = route
	}

	// rules — custom rules prepended (so they match before panel rules)
	if v, ok := customRoute["rules"]; ok {
		if existing, ok := route["rules"].([]M); ok {
			route["rules"] = kernel.MergePrependList(existing, v)
		}
	}

	// rule_set — custom rule_sets appended
	if v, ok := customRoute["rule_set"]; ok {
		if existing, ok := route["rule_set"].([]M); ok {
			route["rule_set"] = kernel.MergeAppendList(existing, v)
		} else {
			route["rule_set"] = kernel.MergeAppendList(nil, v)
		}
	}

	// final, auto_detect_interface, default_interface, etc. — custom overrides
	for k, v := range customRoute {
		if k == "rules" || k == "rule_set" {
			continue
		}
		route[k] = v
	}
}

func buildInbound(nc *model.NodeSpec, users []model.UserSpec, tc kernel.TLSCert) M {
	listenAddr := "::"
	if nc.ListenIP != "" {
		listenAddr = nc.ListenIP
	}
	base := M{
		"tag":         nc.Protocol + "-in",
		"listen":      listenAddr,
		"listen_port": nc.ServerPort,
	}

	switch nc.Protocol {
	case "shadowsocks":
		return buildShadowsocks(base, nc, users)
	case "vmess":
		return buildVMess(base, nc, users, tc)
	case "vless":
		return buildVLESS(base, nc, users, tc)
	case "trojan":
		return buildTrojan(base, nc, users, tc)
	case "hysteria", "hysteria2":
		return buildHysteria(base, nc, users, tc)
	case "tuic":
		return buildTUIC(base, nc, users, tc)
	case "anytls":
		return buildAnyTLS(base, nc, users, tc)
	case "naive":
		return buildNaive(base, nc, users, tc)
	case "socks":
		return buildSocks(base, users)
	case "http":
		return buildHTTP(base, nc, users, tc)
	case "mieru":
		return buildMieru(base, nc, users)
	default:
		return nil
	}
}

func buildMieru(base M, nc *model.NodeSpec, users []model.UserSpec) M {
	base["type"] = "mieru"
	if nc.Transport != "" {
		base["transport"] = nc.Transport
	}
	if nc.TrafficPattern != "" {
		base["traffic_pattern"] = nc.TrafficPattern
	}

	userList := make([]M, 0, len(users))
	for _, u := range users {
		userList = append(userList, M{
			"name":     u.UUID,
			"password": u.UUID,
		})
	}
	base["users"] = userList
	return base
}

type ss2022Config struct {
	method string
	size   int
}

var ss2022Methods = map[string]ss2022Config{
	"2022-blake3-aes-128-gcm":       {"2022-blake3-aes-128-gcm", 16},
	"2022-blake3-aes-256-gcm":       {"2022-blake3-aes-256-gcm", 32},
	"2022-blake3-chacha20-poly1305": {"2022-blake3-chacha20-poly1305", 32},
}

func buildShadowsocks(base M, nc *model.NodeSpec, users []model.UserSpec) M {
	base["type"] = "shadowsocks"
	base["method"] = nc.Cipher

	ss2022, isSS2022 := ss2022Methods[nc.Cipher]
	if isSS2022 {
		base["password"] = nc.ServerKey
	}

	userList := make([]M, len(users))
	var rawBuf []byte
	if isSS2022 {
		rawBuf = make([]byte, ss2022.size)
	}

	for i := range users {
		u := &users[i]
		user := M{
			"name":     u.UUID,
			"password": u.UUID,
		}

		if isSS2022 {
			// Reuse buffer and clear it to maintain SS2022 key integrity
			for j := range rawBuf {
				rawBuf[j] = 0
			}
			copy(rawBuf, u.UUID)
			user["password"] = base64.StdEncoding.EncodeToString(rawBuf)
		}
		userList[i] = user
	}
	base["users"] = userList

	if nc.Plugin != "" {
		nlog.Core().Warn("sing-box shadowsocks inbound does not support plugin, ignoring", "plugin", nc.Plugin)
	}

	return base
}

func buildVMess(base M, nc *model.NodeSpec, users []model.UserSpec, tc kernel.TLSCert) M {
	base["type"] = "vmess"

	userList := make([]M, 0, len(users))
	for _, u := range users {
		userList = append(userList, M{
			"name":    u.UUID,
			"uuid":    u.UUID,
			"alterId": 0,
		})
	}
	base["users"] = userList

	applyTransport(base, nc)
	applyProxyProtocol(base, nc)
	applyMultiplex(base, nc)

	if nc.TLS == 1 {
		if tls := buildTLSConfig(nc, tc); tls != nil {
			base["tls"] = tls
		}
	}

	return base
}

func buildVLESS(base M, nc *model.NodeSpec, users []model.UserSpec, tc kernel.TLSCert) M {
	if nc.Decryption != "" && nc.Decryption != "none" {
		nlog.Core().Warn("sing-box does not support VLESS encryption (decryption), use xray kernel for this feature")
	}

	base["type"] = "vless"

	userList := make([]M, 0, len(users))
	for _, u := range users {
		user := M{
			"name": u.UUID,
			"uuid": u.UUID,
		}
		if nc.Flow != "" {
			user["flow"] = nc.Flow
		}
		userList = append(userList, user)
	}
	base["users"] = userList

	applyTransport(base, nc)
	applyProxyProtocol(base, nc)
	applyMultiplex(base, nc)

	if nc.TLS == 1 {
		if tls := buildTLSConfig(nc, tc); tls != nil {
			base["tls"] = tls
		}
	} else if nc.TLS == 2 {
		base["tls"] = buildRealityConfig(nc)
	}

	return base
}

func buildTrojan(base M, nc *model.NodeSpec, users []model.UserSpec, tc kernel.TLSCert) M {
	base["type"] = "trojan"

	userList := make([]M, len(users))
	for i := range users {
		u := &users[i]
		userList[i] = M{
			"name":     u.UUID,
			"password": u.UUID,
		}
	}
	base["users"] = userList

	applyTransport(base, nc)
	applyProxyProtocol(base, nc)
	applyMultiplex(base, nc)

	if nc.TLS == 1 {
		if tls := buildTLSConfig(nc, tc); tls != nil {
			base["tls"] = tls
		}
	} else if nc.TLS == 2 {
		base["tls"] = buildRealityConfig(nc)
	}

	// If still no TLS/Reality but cert paths exist, enable TLS (panel may omit tls=1).
	if _, ok := base["tls"]; !ok {
		if tls := buildTLSConfig(nc, tc); tls != nil {
			base["tls"] = tls
		}
	}

	if base["tls"] == nil {
		nlog.Core().Warn("trojan inbound has no TLS (no certificate paths and no Reality); sing-box may refuse to start until cert_mode provides cert/key or Reality is configured")
	}

	return base
}

func buildHysteria(base M, nc *model.NodeSpec, users []model.UserSpec, tc kernel.TLSCert) M {
	if nc.Protocol == "hysteria2" || nc.Version == 2 {
		base["type"] = "hysteria2"

		userList := make([]M, 0, len(users))
		for _, u := range users {
			userList = append(userList, M{
				"name":     u.UUID,
				"password": u.UUID,
			})
		}
		base["users"] = userList
		if nc.UpMbps > 0 {
			base["up_mbps"] = nc.UpMbps
		}
		if nc.DownMbps > 0 {
			base["down_mbps"] = nc.DownMbps
		}

		obfsType := strings.TrimSpace(nc.Obfs)
		if obfsType != "" && !strings.EqualFold(obfsType, "none") && strings.TrimSpace(nc.ObfsPassword) != "" {
			base["obfs"] = M{
				"type":     obfsType,
				"password": nc.ObfsPassword,
			}
		}
	} else {
		base["type"] = "hysteria"

		userList := make([]M, 0, len(users))
		for _, u := range users {
			userList = append(userList, M{
				"name":     u.UUID,
				"auth_str": u.UUID,
			})
		}
		base["users"] = userList
		base["up_mbps"] = nc.UpMbps
		base["down_mbps"] = nc.DownMbps

		if nc.Obfs != "" {
			base["obfs"] = nc.Obfs
		}
	}

	tls := buildTLSConfig(nc, tc)
	if tls == nil {
		nlog.Core().Warn("hysteria requires TLS certificate files on disk; configure cert_mode (self, file, http, dns, or content). Sing-box will not start this inbound without tls.")
		return base
	}
	// Hysteria/Hysteria2 uses QUIC and requires ALPN; default to h3 if not set.
	if _, ok := tls["alpn"]; !ok {
		tls["alpn"] = []string{"h3"}
	}
	base["tls"] = tls
	return base
}

func buildTUIC(base M, nc *model.NodeSpec, users []model.UserSpec, tc kernel.TLSCert) M {
	base["type"] = "tuic"

	userList := make([]M, 0, len(users))
	for _, u := range users {
		userList = append(userList, M{
			"name":     u.UUID,
			"uuid":     u.UUID,
			"password": u.UUID,
		})
	}
	base["users"] = userList

	if nc.CongestionControl != "" {
		base["congestion_control"] = nc.CongestionControl
	}

	tls := buildTLSConfig(nc, tc)
	if tls == nil {
		nlog.Core().Warn("tuic requires TLS certificate files on disk; configure cert_mode (self, file, http, dns, or content). Sing-box will not start this inbound without tls.")
		return base
	}
	// TUIC requires ALPN for QUIC negotiation; default to h3 if not set by panel.
	if _, ok := tls["alpn"]; !ok {
		tls["alpn"] = []string{"h3"}
	}
	base["tls"] = tls
	return base
}

func buildAnyTLS(base M, nc *model.NodeSpec, users []model.UserSpec, tc kernel.TLSCert) M {
	base["type"] = "anytls"

	userList := make([]M, 0, len(users))
	for _, u := range users {
		userList = append(userList, M{
			"name":     u.UUID,
			"password": u.UUID,
		})
	}
	base["users"] = userList

	if nc.PaddingScheme != "" {
		base["padding_scheme"] = nc.PaddingScheme
	}

	if tls := buildTLSConfig(nc, tc); tls != nil {
		base["tls"] = tls
	} else {
		nlog.Core().Warn("anytls requires TLS certificate files on disk; configure cert_mode (self, file, http, dns, or content). Sing-box will not start this inbound without tls.")
	}
	return base
}

func buildNaive(base M, nc *model.NodeSpec, users []model.UserSpec, tc kernel.TLSCert) M {
	base["type"] = "naive"

	userList := make([]M, 0, len(users))
	for _, u := range users {
		userList = append(userList, M{
			"username": strconv.Itoa(u.ID),
			"password": u.UUID,
		})
	}
	base["users"] = userList

	if tc.HasCert() {
		if tls := buildTLSConfig(nc, tc); tls != nil {
			base["tls"] = tls
		}
	}
	// sing-box naive defaults to TCP+UDP(QUIC), and QUIC requires TLS; restrict
	// to TCP when TLS is not configured so startup does not fail.
	if _, hasTLS := base["tls"]; !hasTLS {
		base["network"] = "tcp"
	}
	return base
}

func buildSocks(base M, users []model.UserSpec) M {
	base["type"] = "socks"

	userList := make([]M, 0, len(users))
	for _, u := range users {
		userList = append(userList, M{
			"username": u.UUID,
			"password": u.UUID,
		})
	}
	base["users"] = userList
	return base
}

func buildHTTP(base M, nc *model.NodeSpec, users []model.UserSpec, tc kernel.TLSCert) M {
	base["type"] = "http"

	userList := make([]M, 0, len(users))
	for _, u := range users {
		userList = append(userList, M{
			"username": u.UUID,
			"password": u.UUID,
		})
	}
	base["users"] = userList

	applyProxyProtocol(base, nc)
	if nc.TLS == 1 {
		if tls := buildTLSConfig(nc, tc); tls != nil {
			base["tls"] = tls
		}
	}
	return base
}

func applyTransport(base M, nc *model.NodeSpec) {
	netType := strings.ToLower(strings.TrimSpace(nc.Network))
	if netType == "" || netType == "tcp" || netType == "raw" {
		return
	}

	transport := M{"type": netType}

	if nc.NetworkSettings != nil {
		switch netType {
		case "ws":
			if v, ok := nc.NetworkSettings["path"]; ok {
				transport["path"] = v
			}
			var headers M
			if v, ok := nc.NetworkSettings["headers"]; ok {
				if hm, ok := v.(map[string]any); ok && len(hm) > 0 {
					headers = make(M, len(hm)+1)
					for k, val := range hm {
						headers[k] = val
					}
				}
			}
			if v, ok := nc.NetworkSettings["host"]; ok {
				if hostStr := strings.TrimSpace(fmt.Sprintf("%v", v)); hostStr != "" {
					if headers == nil {
						headers = M{}
					}
					if _, hasHost := headers["Host"]; !hasHost {
						headers["Host"] = hostStr
					}
				}
			}
			if len(headers) > 0 {
				transport["headers"] = headers
			}
			if v, ok := nc.NetworkSettings["max_early_data"]; ok {
				transport["max_early_data"] = v
			}
			if v, ok := nc.NetworkSettings["early_data_header_name"]; ok {
				transport["early_data_header_name"] = v
			}
		case "grpc":
			if v, ok := nc.NetworkSettings["serviceName"]; ok {
				transport["service_name"] = v
			} else if v, ok := nc.NetworkSettings["service_name"]; ok {
				transport["service_name"] = v
			}
		case "httpupgrade":
			if v, ok := nc.NetworkSettings["path"]; ok {
				transport["path"] = v
			}
			if v, ok := nc.NetworkSettings["host"]; ok {
				transport["host"] = v
			}
		case "h2", "http":
			transport["type"] = "http"
			if v, ok := nc.NetworkSettings["path"]; ok {
				transport["path"] = v
			}
			if v, ok := nc.NetworkSettings["host"]; ok {
				transport["host"] = v
			}
		}
	}

	base["transport"] = transport
}

// buildTLSConfig returns sing-box inbound TLS options when certificate material
// is available. Returns nil if no TLS material is provided.
func buildTLSConfig(nc *model.NodeSpec, tc kernel.TLSCert) M {
	if !tc.HasCert() {
		return nil
	}

	t := M{"enabled": true}

	serverName := nc.ServerName
	if serverName == "" && nc.Host != "" {
		serverName = nc.Host
	}
	if serverName != "" {
		t["server_name"] = serverName
	}

	if nc.TLSSettings != nil {
		if sn, ok := nc.TLSSettings["server_name"]; ok && sn != "" {
			t["server_name"] = sn
		}
		if alpn, ok := nc.TLSSettings["alpn"]; ok {
			t["alpn"] = alpn
		}
		// ECH server-side: only key/key_path needed for inbound
		if ech := extractECHInbound(nc.TLSSettings); ech != nil {
			t["ech"] = ech
		}
	}

	t["certificate"] = []string{string(tc.CertPEM)}
	t["key"] = []string{string(tc.KeyPEM)}

	return t
}

func buildRealityConfig(nc *model.NodeSpec) M {
	tls := M{"enabled": true}
	if nc.TLSSettings == nil {
		return tls
	}

	reality := M{"enabled": true}
	var settings struct {
		PrivateKey string `mapstructure:"private_key"`
		ShortID    any    `mapstructure:"short_id"`
		Dest       string `mapstructure:"dest"`
		ServerName string `mapstructure:"server_name"`
		ServerPort int    `mapstructure:"server_port"`
	}

	decoderConfig := &mapstructure.DecoderConfig{
		Metadata:         nil,
		Result:           &settings,
		WeaklyTypedInput: true,
	}
	decoder, _ := mapstructure.NewDecoder(decoderConfig)
	_ = decoder.Decode(nc.TLSSettings)

	if settings.PrivateKey != "" {
		reality["private_key"] = settings.PrivateKey
	}

	switch v := settings.ShortID.(type) {
	case string:
		reality["short_id"] = []string{v}
	case []any:
		ids := make([]string, 0, len(v))
		for _, item := range v {
			ids = append(ids, fmt.Sprintf("%v", item))
		}
		reality["short_id"] = ids
	case []string:
		reality["short_id"] = v
	}

	dest := settings.Dest
	if dest == "" {
		dest = settings.ServerName
	}

	if dest != "" {
		defaultPort := 443
		if settings.ServerPort > 0 {
			defaultPort = settings.ServerPort
		}
		handshake := M{"server": dest, "server_port": defaultPort}
		if host, portStr, err := net.SplitHostPort(dest); err == nil {
			handshake["server"] = host
			if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
				handshake["server_port"] = p
			}
		} else {
			// Strip optional brackets on bare IPv6 dest like "[2001:db8::1]".
			handshake["server"] = strings.Trim(dest, "[]")
		}
		reality["handshake"] = handshake
	}

	if settings.ServerName != "" {
		tls["server_name"] = settings.ServerName
	}

	tls["reality"] = reality
	return tls
}

func applyMultiplex(base M, nc *model.NodeSpec) {
	if nc.Multiplex == nil || !nc.Multiplex.Enabled {
		return
	}

	mux := M{
		"enabled": true,
	}
	if nc.Multiplex.Padding {
		mux["padding"] = true
	}

	if nc.Multiplex.Brutal != nil && nc.Multiplex.Brutal.Enabled {
		brutal := M{
			"enabled": true,
		}
		if nc.Multiplex.Brutal.UpMbps > 0 {
			brutal["up_mbps"] = nc.Multiplex.Brutal.UpMbps
		}
		if nc.Multiplex.Brutal.DownMbps > 0 {
			brutal["down_mbps"] = nc.Multiplex.Brutal.DownMbps
		}
		mux["brutal"] = brutal
	}

	base["multiplex"] = mux
}

func applyProxyProtocol(base M, nc *model.NodeSpec) {
	// if !nc.GetProxyProtocol() {
	// 	return
	// }
	// base["proxy_protocol"] = true
}

// extractECHInbound extracts ECH config for sing-box server (inbound).
// sing-box InboundECHOptions needs: enabled, key (PEM array), key_path.
func extractECHInbound(tlsSettings map[string]interface{}) M {
	echRaw, ok := tlsSettings["ech"]
	if !ok {
		return nil
	}
	echMap, ok := echRaw.(map[string]interface{})
	if !ok {
		return nil
	}
	enabled, _ := echMap["enabled"].(bool)
	if !enabled {
		return nil
	}
	ech := M{"enabled": true}
	hasSource := false
	if key, _ := echMap["key"].(string); key != "" {
		ech["key"] = []string{key} // sing-box expects PEM as-is
		hasSource = true
	}
	if keyPath, _ := echMap["key_path"].(string); keyPath != "" {
		ech["key_path"] = keyPath
		hasSource = true
	}
	if !hasSource {
		nlog.Core().Warn("ECH enabled but no key or key_path provided")
	}
	return ech // let sing-box report a clear error if key is missing
}
