package executor

import (
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// applyPayloadConfigWithRoot behaves like applyPayloadConfig but treats all parameter
// paths as relative to the provided root path (for example, "request" for Gemini CLI)
// and restricts matches to the given protocol when supplied. Defaults are checked
// against the original payload when provided. requestedModel carries the client-visible
// model name before alias resolution so payload rules can target aliases precisely.
func applyPayloadConfigWithRoot(cfg *config.Config, model, protocol, root string, payload, original []byte, requestedModel string) []byte {
	if cfg == nil || len(payload) == 0 {
		return payload
	}
	rules := cfg.Payload
	if len(rules.Default) == 0 && len(rules.DefaultRaw) == 0 && len(rules.Override) == 0 && len(rules.OverrideRaw) == 0 && len(rules.Filter) == 0 {
		return payload
	}
	model = strings.TrimSpace(model)
	requestedModel = strings.TrimSpace(requestedModel)
	if model == "" && requestedModel == "" {
		return payload
	}
	candidates := payloadModelCandidates(model, requestedModel)
	out := payload
	source := original
	if len(source) == 0 {
		source = payload
	}
	appliedDefaults := make(map[string]struct{})
	// Apply default rules: first write wins per field across all matching rules.
	for i := range rules.Default {
		rule := &rules.Default[i]
		if !payloadModelRulesMatch(rule.Models, protocol, candidates) {
			continue
		}
		for path, value := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}
			if gjson.GetBytes(source, fullPath).Exists() {
				continue
			}
			if _, ok := appliedDefaults[fullPath]; ok {
				continue
			}
			updated, errSet := sjson.SetBytes(out, fullPath, value)
			if errSet != nil {
				continue
			}
			out = updated
			appliedDefaults[fullPath] = struct{}{}
		}
	}
	// Apply default raw rules: first write wins per field across all matching rules.
	for i := range rules.DefaultRaw {
		rule := &rules.DefaultRaw[i]
		if !payloadModelRulesMatch(rule.Models, protocol, candidates) {
			continue
		}
		for path, value := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}
			if gjson.GetBytes(source, fullPath).Exists() {
				continue
			}
			if _, ok := appliedDefaults[fullPath]; ok {
				continue
			}
			rawValue, ok := payloadRawValue(value)
			if !ok {
				continue
			}
			updated, errSet := sjson.SetRawBytes(out, fullPath, rawValue)
			if errSet != nil {
				continue
			}
			out = updated
			appliedDefaults[fullPath] = struct{}{}
		}
	}
	// Apply override rules: last write wins per field across all matching rules.
	for i := range rules.Override {
		rule := &rules.Override[i]
		if !payloadModelRulesMatch(rule.Models, protocol, candidates) {
			continue
		}
		for path, value := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}
			updated, errSet := sjson.SetBytes(out, fullPath, value)
			if errSet != nil {
				continue
			}
			out = updated
		}
	}
	// Apply override raw rules: last write wins per field across all matching rules.
	for i := range rules.OverrideRaw {
		rule := &rules.OverrideRaw[i]
		if !payloadModelRulesMatch(rule.Models, protocol, candidates) {
			continue
		}
		for path, value := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}
			rawValue, ok := payloadRawValue(value)
			if !ok {
				continue
			}
			updated, errSet := sjson.SetRawBytes(out, fullPath, rawValue)
			if errSet != nil {
				continue
			}
			out = updated
		}
	}
	// Apply filter rules: remove matching paths from payload.
	for i := range rules.Filter {
		rule := &rules.Filter[i]
		if !payloadModelRulesMatch(rule.Models, protocol, candidates) {
			continue
		}
		for _, path := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}
			updated, errDel := sjson.DeleteBytes(out, fullPath)
			if errDel != nil {
				continue
			}
			out = updated
		}
		// Apply array element filters for this rule.
		for _, aeFilter := range rule.ArrayElementFilter {
			fullPath := buildPayloadPath(root, aeFilter.Path)
			if fullPath == "" {
				continue
			}
			out = applyArrayElementFilter(out, fullPath, aeFilter.Match)
		}
	}
	return out
}

func payloadModelRulesMatch(rules []config.PayloadModelRule, protocol string, models []string) bool {
	if len(rules) == 0 || len(models) == 0 {
		return false
	}
	for _, model := range models {
		for _, entry := range rules {
			name := strings.TrimSpace(entry.Name)
			if name == "" {
				continue
			}
			if ep := strings.TrimSpace(entry.Protocol); ep != "" && protocol != "" && !strings.EqualFold(ep, protocol) {
				continue
			}
			if matchModelPattern(name, model) {
				return true
			}
		}
	}
	return false
}

func payloadModelCandidates(model, requestedModel string) []string {
	model = strings.TrimSpace(model)
	requestedModel = strings.TrimSpace(requestedModel)
	if model == "" && requestedModel == "" {
		return nil
	}
	candidates := make([]string, 0, 3)
	seen := make(map[string]struct{}, 3)
	addCandidate := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, value)
	}
	if model != "" {
		addCandidate(model)
	}
	if requestedModel != "" {
		parsed := thinking.ParseSuffix(requestedModel)
		base := strings.TrimSpace(parsed.ModelName)
		if base != "" {
			addCandidate(base)
		}
		if parsed.HasSuffix {
			addCandidate(requestedModel)
		}
	}
	return candidates
}

// buildPayloadPath combines an optional root path with a relative parameter path.
// When root is empty, the parameter path is used as-is. When root is non-empty,
// the parameter path is treated as relative to root.
func buildPayloadPath(root, path string) string {
	r := strings.TrimSpace(root)
	p := strings.TrimSpace(path)
	if r == "" {
		return p
	}
	if p == "" {
		return r
	}
	if strings.HasPrefix(p, ".") {
		p = p[1:]
	}
	return r + "." + p
}

func payloadRawValue(value any) ([]byte, bool) {
	if value == nil {
		return nil, false
	}
	switch typed := value.(type) {
	case string:
		return []byte(typed), true
	case []byte:
		return typed, true
	default:
		raw, errMarshal := json.Marshal(typed)
		if errMarshal != nil {
			return nil, false
		}
		return raw, true
	}
}

func payloadRequestedModel(opts cliproxyexecutor.Options, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if len(opts.Metadata) == 0 {
		return fallback
	}
	raw, ok := opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey]
	if !ok || raw == nil {
		return fallback
	}
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return fallback
		}
		return strings.TrimSpace(v)
	case []byte:
		if len(v) == 0 {
			return fallback
		}
		trimmed := strings.TrimSpace(string(v))
		if trimmed == "" {
			return fallback
		}
		return trimmed
	default:
		return fallback
	}
}

// matchModelPattern performs simple wildcard matching where '*' matches zero or more characters.
// Examples:
//
//	"*-5" matches "gpt-5"
//	"gpt-*" matches "gpt-5" and "gpt-4"
//	"gemini-*-pro" matches "gemini-2.5-pro" and "gemini-3-pro".
func matchModelPattern(pattern, model string) bool {
	pattern = strings.TrimSpace(pattern)
	model = strings.TrimSpace(model)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	// Iterative glob-style matcher supporting only '*' wildcard.
	pi, si := 0, 0
	starIdx := -1
	matchIdx := 0
	for si < len(model) {
		if pi < len(pattern) && (pattern[pi] == model[si]) {
			pi++
			si++
			continue
		}
		if pi < len(pattern) && pattern[pi] == '*' {
			starIdx = pi
			matchIdx = si
			pi++
			continue
		}
		if starIdx != -1 {
			pi = starIdx + 1
			matchIdx++
			si = matchIdx
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

func applyArrayElementFilter(payload []byte, arrayPath string, match config.ElementMatch) []byte {
	arr := gjson.GetBytes(payload, arrayPath)
	if !arr.Exists() || !arr.IsArray() {
		log.Debugf("array-element-filter: path %q not found or not an array, skipping", arrayPath)
		return payload
	}

	key := strings.TrimSpace(match.Key)
	if key == "" {
		log.Debug("array-element-filter: empty match key, skipping filter")
		return payload
	}

	filtered := make([]json.RawMessage, 0)
	for _, elem := range arr.Array() {
		if !elem.IsObject() {
			filtered = append(filtered, json.RawMessage(elem.Raw))
			continue
		}
		keyVal := elem.Get(key)
		if !keyVal.Exists() {
			filtered = append(filtered, json.RawMessage(elem.Raw))
			continue
		}
		if match.Value == nil {
			continue
		}
		if !elementMatchValueEquals(keyVal, match.Value) {
			filtered = append(filtered, json.RawMessage(elem.Raw))
		}
	}

	rebuilt, err := json.Marshal(filtered)
	if err != nil {
		return payload
	}
	updated, err := sjson.SetRawBytes(payload, arrayPath, rebuilt)
	if err != nil {
		return payload
	}
	return updated
}

func elementMatchValueEquals(actual gjson.Result, expected any) bool {
	if expected == nil {
		return true
	}

	actualVal := actual.Value()

	switch exp := expected.(type) {
	case bool:
		if av, ok := actualVal.(bool); ok {
			return av == exp
		}
		return false
	case float64:
		switch av := actualVal.(type) {
		case float64:
			return av == exp
		case int:
			return float64(av) == exp
		case int64:
			return float64(av) == exp
		}
		return false
	case int:
		switch av := actualVal.(type) {
		case float64:
			return av == float64(exp)
		case int:
			return av == exp
		case int64:
			return int64(av) == int64(exp)
		}
		return false
	case int64:
		switch av := actualVal.(type) {
		case float64:
			return av == float64(exp)
		case int:
			return int64(av) == exp
		case int64:
			return av == exp
		}
		return false
	case string:
		if av, ok := actualVal.(string); ok {
			return av == exp
		}
		return false
	case nil:
		return actualVal == nil
	case map[string]any, map[any]any, []any:
		return deepEqualJSON(actualVal, normalizeMapKeys(exp))
	default:
		return deepEqualJSON(actualVal, exp)
	}
}

func normalizeMapKeys(v any) any {
	switch m := v.(type) {
	case map[any]any:
		result := make(map[string]any, len(m))
		for k, val := range m {
			if ks, ok := k.(string); ok {
				result[ks] = normalizeMapKeys(val)
			}
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(m))
		for k, val := range m {
			result[k] = normalizeMapKeys(val)
		}
		return result
	case []any:
		result := make([]any, len(m))
		for i, val := range m {
			result[i] = normalizeMapKeys(val)
		}
		return result
	default:
		return v
	}
}

func deepEqualJSON(a, b any) bool {
	switch av := a.(type) {
	case nil:
		return b == nil
	case bool:
		if bv, ok := b.(bool); ok {
			return av == bv
		}
		return false
	case float64:
		switch bv := b.(type) {
		case float64:
			return av == bv
		case int:
			return av == float64(bv)
		case int64:
			return av == float64(bv)
		}
		return false
	case string:
		if bv, ok := b.(string); ok {
			return av == bv
		}
		return false
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !deepEqualJSON(av[i], bv[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, va := range av {
			vb, exists := bv[k]
			if !exists || !deepEqualJSON(va, vb) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

func normalizeJSON(data []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
