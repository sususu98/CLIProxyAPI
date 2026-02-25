package auth

import (
	"strings"

	log "github.com/sirupsen/logrus"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// fallbackController manages the model fallback state machine.
//
// When the primary model fails on ALL auth credentials (exhausting
// max-auth-rotations and request-retry), the controller triggers a
// fallback to the configured alternative model. This covers both
// 503 capacity-exhausted and 429 rate-limit scenarios.
//
// Auth rotation is always attempted first because different auth
// accounts may be routed through different regions/proxies, so
// capacity or rate limits may be region-specific.
type fallbackController struct {
	routeModel      string
	pendingFallback string
	aliasResult     OAuthModelAliasResult
	used            bool
}

func newFallbackController(routeModel string) fallbackController {
	return fallbackController{routeModel: routeModel}
}

// Capture records the fallback model from alias resolution.
// Only the first non-empty fallback is captured; subsequent calls are no-ops.
func (fc *fallbackController) Capture(result OAuthModelAliasResult) {
	if fc.used || fc.pendingFallback != "" || result.FallbackModel == "" {
		return
	}
	fc.pendingFallback = result.FallbackModel
	fc.aliasResult = result
}

// ShouldFallback returns true when all auths are exhausted and the last
// error was a model-unavailable error (503 capacity or 429 rate limit).
// The caller should invoke this at auth-exhaustion boundaries:
//   - when authRotationCount >= maxAuthRotations
//   - when pickNext returns no more auths (errPick != nil)
func (fc *fallbackController) ShouldFallback(lastErr error) bool {
	return !fc.used && fc.pendingFallback != "" && isModelUnavailableError(lastErr)
}

// Activate transitions to fallback mode and logs the transition.
// The caller must reset authRotationCount, tried map, and lastErr.
func (fc *fallbackController) Activate(context string) {
	log.Debugf("conductor: %s, falling back to %s", context, fc.pendingFallback)
	fc.used = true
}

// ApplyModel overrides the request model with the fallback model when active.
func (fc *fallbackController) ApplyModel(execReq *cliproxyexecutor.Request) {
	if fc.used {
		execReq.Model = fc.pendingFallback
	}
}

// PostProcessResponse applies response post-processing (StripThinking, ForceMapping).
// When fallback is active, uses the original alias's post-processing config
// so the client sees consistent model names and response format.
func (fc *fallbackController) PostProcessResponse(resp *cliproxyexecutor.Response, currentAlias OAuthModelAliasResult) {
	effective := fc.effectiveAlias(currentAlias)
	if effective.StripThinkingResponse {
		resp.Payload = stripThinkingBlocksFromResponse(resp.Payload)
	}
	if effective.ForceMapping && effective.OriginalAlias != "" {
		resp.Payload = rewriteModelInResponse(resp.Payload, effective.OriginalAlias)
	}
}

// EffectiveAlias returns the alias result to use for stream post-processing.
// When fallback is active, returns the original alias result captured at
// fallback configuration time, not the current iteration's alias.
func (fc *fallbackController) EffectiveAlias(currentAlias OAuthModelAliasResult) OAuthModelAliasResult {
	return fc.effectiveAlias(currentAlias)
}

func (fc *fallbackController) effectiveAlias(currentAlias OAuthModelAliasResult) OAuthModelAliasResult {
	if fc.used {
		return fc.aliasResult
	}
	return currentAlias
}

// Available returns true if a fallback model is configured.
func (fc *fallbackController) Available() bool {
	return fc.pendingFallback != ""
}

// Used returns true if fallback has been activated.
func (fc *fallbackController) Used() bool {
	return fc.used
}

// isCapacityExhaustedError returns true for 503 errors indicating model-level
// capacity exhaustion. Kept separate from isModelUnavailableError for potential
// future use in differentiated retry strategies.
func isCapacityExhaustedError(err error) bool {
	if err == nil {
		return false
	}
	status := statusCodeFromError(err)
	if status != 503 {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "capacity") || strings.Contains(msg, "unavailable")
}
