package kernel

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/cedar2025/xboard-node/internal/nlog"
	"gopkg.in/yaml.v3"
)

// LoadCustomConfig reads a custom config file (JSON or YAML) and returns it
// as a generic map with all nested YAML maps normalized to map[string]any so
// encoding/json can always marshal the result.
// Returns nil if path is empty or file does not exist.
func LoadCustomConfig(path string) (map[string]any, error) {
	if path == "" {
		return nil, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			nlog.Core().Warn("custom config file not found, skipping", "path", path)
			return nil, nil
		}
		return nil, fmt.Errorf("read custom config %s: %w", path, err)
	}

	if len(data) == 0 {
		return nil, nil
	}

	result := make(map[string]any)

	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "{") {
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("parse custom config as JSON: %w", err)
		}
	} else {
		var raw any
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("parse custom config as YAML: %w", err)
		}
		if normalized, ok := normalizeJSONValue(raw).(map[string]any); ok {
			result = normalized
		}
	}

	nlog.Core().Info("loaded custom config", "path", path, "keys", mapKeys(result))
	return result, nil
}

// normalizeJSONValue recursively converts map[any]any (produced by yaml.v3 for
// non-string keys) into map[string]any so json.Marshal never fails.
func normalizeJSONValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			out[k] = normalizeJSONValue(item)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			out[fmt.Sprint(k)] = normalizeJSONValue(item)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = normalizeJSONValue(item)
		}
		return out
	default:
		return v
	}
}

// MergeAppendList appends items from src list into a freshly allocated copy of
// dst, returning the merged list without mutating caller slice backing arrays.
func MergeAppendList(dst []map[string]any, srcRaw any) []map[string]any {
	srcList, ok := toSliceOfMaps(srcRaw)
	if !ok {
		return dst
	}
	out := make([]map[string]any, 0, len(dst)+len(srcList))
	out = append(out, dst...)
	out = append(out, srcList...)
	return out
}

// MergePrependList prepends items from src list before dst items into a freshly
// allocated slice without mutating srcRaw's or dst's backing arrays.
func MergePrependList(dst []map[string]any, srcRaw any) []map[string]any {
	srcList, ok := toSliceOfMaps(srcRaw)
	if !ok {
		return dst
	}
	out := make([]map[string]any, 0, len(srcList)+len(dst))
	out = append(out, srcList...)
	out = append(out, dst...)
	return out
}

// CustomConfigOutboundTags extracts exact tag names from top-level "outbounds"
// and "endpoints" arrays of an already-loaded custom config map.
func CustomConfigOutboundTags(custom map[string]any) map[string]bool {
	tags := make(map[string]bool)
	if custom == nil {
		return tags
	}
	for _, section := range []string{"outbounds", "endpoints"} {
		items, ok := toSliceOfMaps(custom[section])
		if !ok {
			continue
		}
		for _, item := range items {
			if tag, ok := item["tag"].(string); ok {
				if trimmed := strings.TrimSpace(tag); trimmed != "" {
					tags[trimmed] = true
				}
			}
		}
	}
	return tags
}

func toSliceOfMaps(v any) ([]map[string]any, bool) {
	if v == nil {
		return nil, false
	}
	switch items := v.(type) {
	case []any:
		result := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if m, ok := toMap(item); ok {
				result = append(result, m)
			}
		}
		return result, len(result) > 0
	case []map[string]any:
		return items, len(items) > 0
	default:
		return nil, false
	}
}

func toMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		result := make(map[string]any, len(m))
		for k, val := range m {
			result[fmt.Sprint(k)] = normalizeJSONValue(val)
		}
		return result, true
	default:
		return nil, false
	}
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
