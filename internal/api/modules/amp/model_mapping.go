// Package amp provides model mapping functionality for routing Amp CLI requests
// to alternative models when the requested model is not available locally.
package amp

import (
	"regexp"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

// MappingResult contains the result of a thinking-aware model mapping.
type MappingResult struct {
	// TargetModel is the model to route the request to.
	TargetModel string
	// StripThinkingResponse indicates whether to remove thinking blocks from response.
	StripThinkingResponse bool
}

// ModelMapper provides model name mapping/aliasing for Amp CLI requests.
// When an Amp request comes in for a model that isn't available locally,
// this mapper can redirect it to an alternative model that IS available.
type ModelMapper interface {
	// MapModel returns the target model name if a mapping exists and the target
	// model has available providers. Returns empty string if no mapping applies.
	MapModel(requestedModel string) string

	// MapModelWithThinking returns the target model based on thinking mode.
	// It considers ToThinking/ToNonThinking fields and returns mapping result
	// including whether to strip thinking blocks from response.
	MapModelWithThinking(requestedModel string, thinkingEnabled bool) MappingResult

	// UpdateMappings refreshes the mapping configuration (for hot-reload).
	UpdateMappings(mappings []config.AmpModelMapping)
}

// thinkingAwareMapping stores extended mapping information for thinking-aware routing.
type thinkingAwareMapping struct {
	to                    string
	toThinking            string
	toNonThinking         string
	stripThinkingResponse bool
}

// DefaultModelMapper implements ModelMapper with thread-safe mapping storage.
type DefaultModelMapper struct {
	mu              sync.RWMutex
	mappings        map[string]string              // exact: from -> to (normalized lowercase keys)
	thinkingMapping map[string]thinkingAwareMapping // extended thinking-aware mappings
	regexps         []regexMapping                 // regex rules evaluated in order
}

// NewModelMapper creates a new model mapper with the given initial mappings.
func NewModelMapper(mappings []config.AmpModelMapping) *DefaultModelMapper {
	m := &DefaultModelMapper{
		mappings:        make(map[string]string),
		thinkingMapping: make(map[string]thinkingAwareMapping),
		regexps:         nil,
	}
	m.UpdateMappings(mappings)
	return m
}

// MapModel checks if a mapping exists for the requested model and if the
// target model has available local providers. Returns the mapped model name
// or empty string if no valid mapping exists.
func (m *DefaultModelMapper) MapModel(requestedModel string) string {
	if requestedModel == "" {
		return ""
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	// Normalize the requested model for lookup
	normalizedRequest := strings.ToLower(strings.TrimSpace(requestedModel))

	// Check for direct mapping
	targetModel, exists := m.mappings[normalizedRequest]
	if !exists {
		// Try regex mappings in order
		base, _ := util.NormalizeThinkingModel(requestedModel)
		for _, rm := range m.regexps {
			if rm.re.MatchString(requestedModel) || (base != "" && rm.re.MatchString(base)) {
				targetModel = rm.to
				exists = true
				break
			}
		}
		if !exists {
			return ""
		}
	}

	// Verify target model has available providers
	normalizedTarget, _ := util.NormalizeThinkingModel(targetModel)
	providers := util.GetProviderName(normalizedTarget)
	if len(providers) == 0 {
		log.Debugf("amp model mapping: target model %s has no available providers, skipping mapping", targetModel)
		return ""
	}

	// Note: Detailed routing log is handled by logAmpRouting in fallback_handlers.go
	return targetModel
}

// UpdateMappings refreshes the mapping configuration from config.
// This is called during initialization and on config hot-reload.
func (m *DefaultModelMapper) UpdateMappings(mappings []config.AmpModelMapping) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Clear and rebuild mappings
	m.mappings = make(map[string]string, len(mappings))
	m.thinkingMapping = make(map[string]thinkingAwareMapping, len(mappings))
	m.regexps = make([]regexMapping, 0, len(mappings))

	for _, mapping := range mappings {
		from := strings.TrimSpace(mapping.From)
		to := strings.TrimSpace(mapping.To)
		toThinking := strings.TrimSpace(mapping.ToThinking)
		toNonThinking := strings.TrimSpace(mapping.ToNonThinking)

		// At least one target must be specified
		if from == "" || (to == "" && toThinking == "" && toNonThinking == "") {
			log.Warnf("amp model mapping: skipping invalid mapping (from=%q, to=%q, to-thinking=%q, to-non-thinking=%q)", from, to, toThinking, toNonThinking)
			continue
		}

		if mapping.Regex {
			// Compile case-insensitive regex; wrap with (?i) to match behavior of exact lookups
			pattern := "(?i)" + from
			re, err := regexp.Compile(pattern)
			if err != nil {
				log.Warnf("amp model mapping: invalid regex %q: %v", from, err)
				continue
			}
			m.regexps = append(m.regexps, regexMapping{
				re:                    re,
				to:                    to,
				toThinking:            toThinking,
				toNonThinking:         toNonThinking,
				stripThinkingResponse: mapping.StripThinkingResponse,
			})
			log.Debugf("amp model regex mapping registered: /%s/ -> %s (thinking:%s, non-thinking:%s, strip:%v)",
				from, to, toThinking, toNonThinking, mapping.StripThinkingResponse)
		} else {
			// Store with normalized lowercase key for case-insensitive lookup
			normalizedFrom := strings.ToLower(from)
			m.mappings[normalizedFrom] = to
			m.thinkingMapping[normalizedFrom] = thinkingAwareMapping{
				to:                    to,
				toThinking:            toThinking,
				toNonThinking:         toNonThinking,
				stripThinkingResponse: mapping.StripThinkingResponse,
			}
			log.Debugf("amp model mapping registered: %s -> %s (thinking:%s, non-thinking:%s, strip:%v)",
				from, to, toThinking, toNonThinking, mapping.StripThinkingResponse)
		}
	}

	if len(m.mappings) > 0 {
		log.Infof("amp model mapping: loaded %d mapping(s)", len(m.mappings))
	}
	if n := len(m.regexps); n > 0 {
		log.Infof("amp model mapping: loaded %d regex mapping(s)", n)
	}
}

// GetMappings returns a copy of current mappings (for debugging/status).
func (m *DefaultModelMapper) GetMappings() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]string, len(m.mappings))
	for k, v := range m.mappings {
		result[k] = v
	}
	return result
}

// MapModelWithThinking returns the target model based on thinking mode.
// It considers ToThinking/ToNonThinking fields and returns mapping result
// including whether to strip thinking blocks from response.
func (m *DefaultModelMapper) MapModelWithThinking(requestedModel string, thinkingEnabled bool) MappingResult {
	if requestedModel == "" {
		return MappingResult{}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	// Normalize the requested model for lookup
	normalizedRequest := strings.ToLower(strings.TrimSpace(requestedModel))
	base, _ := util.NormalizeThinkingModel(requestedModel)

	// Check for direct mapping
	tam, exists := m.thinkingMapping[normalizedRequest]
	if !exists {
		// Try base model without thinking suffix
		if base != "" && base != requestedModel {
			normalizedBase := strings.ToLower(strings.TrimSpace(base))
			tam, exists = m.thinkingMapping[normalizedBase]
		}
	}

	var targetModel string
	var stripThinking bool

	if exists {
		// Use thinking-aware mapping
		targetModel, stripThinking = m.resolveThinkingTarget(tam, thinkingEnabled)
	} else {
		// Try regex mappings in order
		for _, rm := range m.regexps {
			if rm.re.MatchString(requestedModel) || (base != "" && rm.re.MatchString(base)) {
				regexTam := thinkingAwareMapping{
					to:                    rm.to,
					toThinking:            rm.toThinking,
					toNonThinking:         rm.toNonThinking,
					stripThinkingResponse: rm.stripThinkingResponse,
				}
				targetModel, stripThinking = m.resolveThinkingTarget(regexTam, thinkingEnabled)
				exists = true
				break
			}
		}
	}

	if !exists || targetModel == "" {
		return MappingResult{}
	}

	// Verify target model has available providers
	normalizedTarget, _ := util.NormalizeThinkingModel(targetModel)
	providers := util.GetProviderName(normalizedTarget)
	if len(providers) == 0 {
		log.Debugf("amp model mapping: target model %s has no available providers, skipping mapping", targetModel)
		return MappingResult{}
	}

	return MappingResult{
		TargetModel:           targetModel,
		StripThinkingResponse: stripThinking,
	}
}

// resolveThinkingTarget determines the target model based on thinking mode.
func (m *DefaultModelMapper) resolveThinkingTarget(tam thinkingAwareMapping, thinkingEnabled bool) (string, bool) {
	if thinkingEnabled {
		// Thinking request: prefer ToThinking, fallback to To
		if tam.toThinking != "" {
			return tam.toThinking, false
		}
		return tam.to, false
	}

	// Non-thinking request: prefer ToNonThinking, fallback to To
	if tam.toNonThinking != "" {
		return tam.toNonThinking, false
	}

	// Using To for non-thinking request - check if we should strip thinking
	return tam.to, tam.stripThinkingResponse
}

type regexMapping struct {
	re                    *regexp.Regexp
	to                    string
	toThinking            string
	toNonThinking         string
	stripThinkingResponse bool
}
