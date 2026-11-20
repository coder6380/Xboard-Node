package kernel

import (
	"strconv"
	"strings"

	"github.com/cedar2025/xboard-node/internal/model"
)

func NeedsGeoIPRules(rules []model.CustomRouteRule) bool {
	for _, r := range rules {
		for _, v := range r.Match.IPCIDRs {
			if strings.HasPrefix(strings.TrimSpace(v), "geoip:") {
				return true
			}
		}
		for _, v := range r.Match.SourceCIDRs {
			if strings.HasPrefix(strings.TrimSpace(v), "geoip:") {
				return true
			}
		}
	}
	return false
}

func NeedsGeoSiteRules(rules []model.CustomRouteRule) bool {
	for _, r := range rules {
		for _, v := range r.Match.Domains {
			if strings.HasPrefix(strings.TrimSpace(v), "geosite:") {
				return true
			}
		}
		for _, v := range r.Match.DomainSuffixes {
			if strings.HasPrefix(strings.TrimSpace(v), "geosite:") {
				return true
			}
		}
	}
	return false
}

// NormalizePortSpecs parses single ports and port ranges (accepting either ":"
// or "-" with optional whitespace, skipping blank entries). Single ports are
// returned in ports, and normalized ranges are formatted using rangeSep
// (":" for sing-box, "-" for xray).
func NormalizePortSpecs(values []string, rangeSep string) (ports []int, ranges []string) {
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if strings.ContainsAny(value, ":-") {
			sep := "-"
			if strings.Contains(value, ":") {
				sep = ":"
			}
			parts := strings.Split(value, sep)
			if len(parts) != 2 {
				continue
			}
			start := strings.TrimSpace(parts[0])
			end := strings.TrimSpace(parts[1])
			if start == "" || end == "" {
				continue
			}
			ranges = append(ranges, start+rangeSep+end)
			continue
		}
		if port, err := strconv.Atoi(value); err == nil {
			ports = append(ports, port)
		}
	}
	return ports, ranges
}

// FormatXrayPortSpecs converts validated port specs into a comma-separated
// Xray port string ("80,443,1000-2000"), trimming spaces and normalizing ":".
func FormatXrayPortSpecs(values []string) string {
	ports, ranges := NormalizePortSpecs(values, "-")
	parts := make([]string, 0, len(ports)+len(ranges))
	for _, p := range ports {
		parts = append(parts, strconv.Itoa(p))
	}
	parts = append(parts, ranges...)
	return strings.Join(parts, ",")
}

// NormalizeNetworks trims, lowercases, and filters blank network names.
func NormalizeNetworks(values []string) []string {
	var out []string
	for _, v := range values {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// TrimNonEmpty trims each element and discards empty strings.
func TrimNonEmpty(values []string) []string {
	var out []string
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
