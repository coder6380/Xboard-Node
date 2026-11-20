package model

import (
	"fmt"
	"strings"

	"github.com/cedar2025/xboard-node/internal/config"
)

func ValidateNodeSpec(n *NodeSpec, kcfg config.KernelConfig) error {
	if n == nil {
		return nil
	}

	effectiveKernelType := strings.TrimSpace(kcfg.Type)
	if effectiveKernelType == "" {
		effectiveKernelType = strings.TrimSpace(n.KernelType)
	}
	kernelType, err := normalizeKernelType(effectiveKernelType)
	if err != nil {
		return fmt.Errorf("normalize kernel type: %w", err)
	}

	additionalOutboundSources, err := collectAdditionalOutboundTagSources(kcfg.CustomConfig, kcfg.CustomOutbound)
	if err != nil {
		return fmt.Errorf("collect additional outbound tags: %w", err)
	}
	if err := validateOutboundTagCollisions(n.CustomOutbounds, additionalOutboundSources); err != nil {
		return fmt.Errorf("validate outbound tags: %w", err)
	}
	additionalTags := additionalTagNames(additionalOutboundSources)
	availableTags := buildAvailableOutboundTags(n.CustomOutbounds, additionalTags)
	if err := ValidateCustomOutboundsForKernel(n.CustomOutbounds, kernelType, additionalTags); err != nil {
		return fmt.Errorf("validate custom outbounds: %w", err)
	}
	if err := ValidateCustomRouteRules(n.CustomRouteRules, kernelType, availableTags); err != nil {
		return fmt.Errorf("validate custom route rules: %w", err)
	}
	if err := validateTransportKernel(n.Network, kernelType); err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(n.Protocol), "shadowsocks") &&
		strings.EqualFold(strings.TrimSpace(n.Cipher), "2022-blake3-chacha20-poly1305") {
		return fmt.Errorf("shadowsocks cipher %q does not support multi-user mode in %s; use 2022-blake3-aes-128-gcm or 2022-blake3-aes-256-gcm", n.Cipher, kernelType)
	}
	return nil
}

// singboxUnsupportedTransports lists transport types that sing-box does not support.
var singboxUnsupportedTransports = map[string]bool{
	"xhttp":     true,
	"splithttp": true,
}

// xrayUnsupportedTransports lists transport types removed from xray-core.
var xrayUnsupportedTransports = map[string]bool{
	"h2":   true,
	"http": true,
	"quic": true,
	"h3":   true,
}

func validateTransportKernel(network, kernelType string) error {
	net := strings.ToLower(strings.TrimSpace(network))
	k := strings.ToLower(strings.TrimSpace(kernelType))
	if (k == "singbox" || k == "sing-box") && singboxUnsupportedTransports[net] {
		return fmt.Errorf("transport %q is not supported by sing-box kernel; use xray kernel instead", net)
	}
	if (k == "xray" || k == "xray-core") && xrayUnsupportedTransports[net] {
		return fmt.Errorf("transport %q is not supported by xray kernel; use singbox kernel instead", net)
	}
	return nil
}

// ResolveKernelForTransport returns the kernel type required by the given
// transport. If the configured kernel cannot handle the transport, it returns
// the kernel that can. Otherwise it returns configuredKernel unchanged.
// This is used in machine mode to auto-switch kernel per node.
func ResolveKernelForTransport(network, configuredKernel string) string {
	net := strings.ToLower(strings.TrimSpace(network))
	k := strings.ToLower(strings.TrimSpace(configuredKernel))
	if (k == "singbox" || k == "sing-box") && singboxUnsupportedTransports[net] {
		return "xray"
	}
	if (k == "xray" || k == "xray-core") && xrayUnsupportedTransports[net] {
		return "singbox"
	}
	return configuredKernel
}

func normalizeKernelType(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "singbox", "sing-box":
		return "singbox", nil
	case "xray", "xray-core":
		return "xray", nil
	default:
		return "", fmt.Errorf("unsupported kernel type %q", value)
	}
}

func buildAvailableOutboundTags(structured []OutboundConfig, rawTags []string) map[string]struct{} {
	available := map[string]struct{}{
		"direct": {},
		"block":  {},
	}
	for _, outbound := range structured {
		tag := strings.TrimSpace(outbound.Tag)
		if tag != "" {
			available[tag] = struct{}{}
		}
	}
	for _, tag := range rawTags {
		tag = strings.TrimSpace(tag)
		if tag != "" {
			available[tag] = struct{}{}
		}
	}
	return available
}
