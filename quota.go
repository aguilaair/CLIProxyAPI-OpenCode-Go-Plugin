package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

const (
	rolling5hWindow     = 5 * time.Hour
	rollingWeeklyWindow = 7 * 24 * time.Hour
	rollingMonthWindow  = 30 * 24 * time.Hour
	maxRecordedEvents   = 20000
)

type openCodeUsageSample struct {
	Recorded      bool
	BillingSource string
	ListCost      float64
	Allowance     float64
	Cost          float64
	InputTokens   int64
	OutputTokens  int64
	ReasoningToks int64
	CacheReadToks int64
}

type allowanceEvent struct {
	Timestamp     time.Time `json:"timestamp"`
	AuthID        string    `json:"auth_id"`
	Model         string    `json:"model"`
	BillingSource string    `json:"billing_source,omitempty"`
	ListCost      float64   `json:"list_cost"`
	Allowance     float64   `json:"allowance"`
	InputTokens   int64     `json:"input_tokens"`
	OutputTokens  int64     `json:"output_tokens"`
	ReasoningToks int64     `json:"reasoning_tokens"`
	CacheReadToks int64     `json:"cache_read_tokens"`
}

var (
	usageMu      sync.RWMutex
	usageEvents  []allowanceEvent
	planByAuthID = make(map[string]string)
)

type rpcQuotaFetchRequest struct {
	pluginapi.QuotaFetchRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func normalizePlanTier(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case s == "go-plus" || s == "goplus" || s == "plus" || strings.Contains(s, "go-plus") || strings.Contains(s, "go plus") || strings.Contains(s, "$40") || s == "40":
		return "go-plus"
	case s == "go" || strings.Contains(s, "$10") || s == "10":
		return "go"
	default:
		return ""
	}
}

func probeAccountPlanTier(hostCallbackID, token, orgID string) string {
	if strings.TrimSpace(hostCallbackID) == "" || strings.TrimSpace(token) == "" {
		return ""
	}
	cfg := currentConfig()
	headers := http.Header{
		"Authorization":      []string{"Bearer " + strings.TrimSpace(token)},
		"Content-Type":       []string{"application/json"},
		"Accept":             []string{"application/json"},
		"User-Agent":         []string{cfg.UserAgent},
		"x-opencode-session": []string{cfg.SessionID},
	}
	if strings.TrimSpace(orgID) != "" {
		headers.Set("x-opencode-org-id", strings.TrimSpace(orgID))
	}
	body := []byte(`{"model":"beam-free","messages":[{"role":"user","content":"hi"}],"max_tokens":1,"stream":false}`)
	resp, errHTTP := doHostHTTP(hostCallbackID, http.MethodPost, cfg.InferenceBaseURL+"/openai/v1/chat/completions", headers, body)
	if errHTTP != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	return normalizePlanTier(gjson.GetBytes(resp.Body, "usage.opencode.billing_source").String())
}

func parseOpenCodeUsageNode(u gjson.Result) (openCodeUsageSample, bool) {
	if !u.Exists() {
		return openCodeUsageSample{}, false
	}
	oc := u.Get("opencode")
	if !oc.Exists() {
		return openCodeUsageSample{}, false
	}
	toks := oc.Get("tokens")
	sample := openCodeUsageSample{
		Recorded:      true,
		BillingSource: strings.TrimSpace(oc.Get("billing_source").String()),
		ListCost:      oc.Get("list_cost").Float(),
		Allowance:     oc.Get("allowance").Float(),
		Cost:          u.Get("cost").Float(),
		InputTokens:   toks.Get("input").Int(),
		OutputTokens:  toks.Get("output").Int(),
		ReasoningToks: toks.Get("reasoning").Int(),
		CacheReadToks: toks.Get("cache_read").Int(),
	}
	if sample.InputTokens == 0 {
		sample.InputTokens = firstPositiveInt64(u.Get("prompt_tokens").Int(), u.Get("input_tokens").Int(), u.Get("promptTokenCount").Int())
	}
	if sample.OutputTokens == 0 {
		sample.OutputTokens = firstPositiveInt64(u.Get("completion_tokens").Int(), u.Get("output_tokens").Int(), u.Get("candidatesTokenCount").Int())
	}
	return sample, true
}

func recordOpenCodeUsagePayload(authID, model string, proto wireProtocol, body []byte) {
	if len(body) == 0 {
		return
	}
	var u gjson.Result
	if proto == wireProtocolGemini {
		u = gjson.GetBytes(body, "usageMetadata")
	} else {
		u = gjson.GetBytes(body, "usage")
	}
	if sample, ok := parseOpenCodeUsageNode(u); ok {
		commitOpenCodeStreamUsage(authID, model, sample)
	}
}

func observeOpenCodeStreamLineUsage(proto wireProtocol, line []byte, dst *openCodeUsageSample) {
	if dst == nil || len(line) == 0 {
		return
	}
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return
	}
	jsonPart := bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
	if len(jsonPart) == 0 || bytes.Equal(jsonPart, []byte("[DONE]")) {
		return
	}
	for _, path := range []string{"usage", "response.usage", "message.usage", "usageMetadata"} {
		if sample, ok := parseOpenCodeUsageNode(gjson.GetBytes(jsonPart, path)); ok {
			*dst = sample
			return
		}
	}
	_ = proto
}

func commitOpenCodeStreamUsage(authID, model string, sample openCodeUsageSample) {
	if !sample.Recorded {
		return
	}
	now := time.Now().UTC()
	model = strings.TrimSpace(model)
	if model == "" {
		model = "unknown"
	}

	usageMu.Lock()
	defer usageMu.Unlock()

	if tier := normalizePlanTier(sample.BillingSource); tier != "" && strings.TrimSpace(authID) != "" {
		planByAuthID[strings.TrimSpace(authID)] = tier
	}

	usageEvents = append(usageEvents, allowanceEvent{
		Timestamp:     now,
		AuthID:        strings.TrimSpace(authID),
		Model:         model,
		BillingSource: sample.BillingSource,
		ListCost:      sample.ListCost,
		Allowance:     sample.Allowance,
		InputTokens:   sample.InputTokens,
		OutputTokens:  sample.OutputTokens,
		ReasoningToks: sample.ReasoningToks,
		CacheReadToks: sample.CacheReadToks,
	})
	if len(usageEvents) > maxRecordedEvents {
		usageEvents = append([]allowanceEvent(nil), usageEvents[len(usageEvents)-maxRecordedEvents:]...)
	}
}

func resolveAllowanceCapsForPlan(plan string) (planLabel string, cap5h, capWeekly, capMonth float64) {
	cfg := currentConfig()
	if normalizePlanTier(plan) == "go-plus" {
		planLabel = "OpenCode Go Plus ($40/mo)"
		cap5h = defaultGoPlus5h
		capWeekly = defaultGoPlusWeekly
		capMonth = defaultGoPlusMonth
	} else {
		planLabel = "OpenCode Go ($10/mo)"
		cap5h = defaultGoAllowance5h
		capWeekly = defaultGoAllowanceWeekly
		capMonth = defaultGoAllowanceMonth
	}
	if cfg.Allowance5h > 0 {
		cap5h = cfg.Allowance5h
	}
	if cfg.AllowanceWeekly > 0 {
		capWeekly = cfg.AllowanceWeekly
	}
	if cfg.AllowanceMonthly > 0 {
		capMonth = cfg.AllowanceMonthly
	}
	return planLabel, cap5h, capWeekly, capMonth
}

func resolveAllowanceCaps() (planLabel string, cap5h, capWeekly, capMonth float64) {
	return resolveAllowanceCapsForPlan(currentConfig().Plan)
}

func extractPlanOverrideFromAuth(req rpcQuotaFetchRequest) string {
	if req.Attributes != nil {
		for _, k := range []string{"header:x-opencode-plan", "plan", "plan_type", "note"} {
			if tier := normalizePlanTier(req.Attributes[k]); tier != "" {
				return tier
			}
		}
	}
	if req.Metadata != nil {
		for _, k := range []string{"note", "plan", "plan_type"} {
			if tier := normalizePlanTier(stringFromMap(req.Metadata, k)); tier != "" {
				return tier
			}
		}
		if hdrs, ok := req.Metadata["headers"].(map[string]any); ok {
			if tier := normalizePlanTier(stringFromMap(hdrs, "x-opencode-plan", "X-Opencode-Plan")); tier != "" {
				return tier
			}
		}
	}
	if len(req.StorageJSON) > 0 {
		for _, path := range []string{"note", "plan", "plan_type", "headers.x-opencode-plan", "headers.X-Opencode-Plan"} {
			if tier := normalizePlanTier(gjson.GetBytes(req.StorageJSON, path).String()); tier != "" {
				return tier
			}
		}
	}
	return ""
}

func resolveAllowanceCapsForAuth(req rpcQuotaFetchRequest) (planLabel string, cap5h, capWeekly, capMonth float64) {
	// 1. Explicit per-credential override in Auth File Details / Edit (Note, Headers x-opencode-plan, or plan field in JSON)
	if override := extractPlanOverrideFromAuth(req); override != "" {
		return resolveAllowanceCapsForPlan(override)
	}
	authID := strings.TrimSpace(req.AuthID)
	if authID != "" {
		usageMu.RLock()
		cached := planByAuthID[authID]
		usageMu.RUnlock()
		if cached != "" {
			return resolveAllowanceCapsForPlan(cached)
		}
	}
	// 2. Live $0.00 probe on beam-free for this specific credential
	token, orgID := extractAuthCredentials(req.StorageJSON, req.Attributes)
	if probed := probeAccountPlanTier(req.HostCallbackID, token, orgID); probed != "" {
		if authID != "" {
			usageMu.Lock()
			planByAuthID[authID] = probed
			usageMu.Unlock()
		}
		return resolveAllowanceCapsForPlan(probed)
	}
	return resolveAllowanceCaps()
}

func computeWindowTotals(authID string, now time.Time) (used5h, used7d, used30d float64, oldest5h, oldest7d, oldest30d time.Time) {
	cutoff5h := now.Add(-rolling5hWindow)
	cutoff7d := now.Add(-rollingWeeklyWindow)
	cutoff30d := now.Add(-rollingMonthWindow)

	usageMu.RLock()
	defer usageMu.RUnlock()

	for _, ev := range usageEvents {
		if authID != "" && ev.AuthID != "" && ev.AuthID != authID {
			continue
		}
		if ev.Timestamp.After(cutoff30d) {
			used30d += ev.Allowance
			if oldest30d.IsZero() || ev.Timestamp.Before(oldest30d) {
				oldest30d = ev.Timestamp
			}
		}
		if ev.Timestamp.After(cutoff7d) {
			used7d += ev.Allowance
			if oldest7d.IsZero() || ev.Timestamp.Before(oldest7d) {
				oldest7d = ev.Timestamp
			}
		}
		if ev.Timestamp.After(cutoff5h) {
			used5h += ev.Allowance
			if oldest5h.IsZero() || ev.Timestamp.Before(oldest5h) {
				oldest5h = ev.Timestamp
			}
		}
	}
	return
}

func handleQuotaDescribe(_ []byte) ([]byte, error) {
	return okEnvelope(pluginapi.QuotaDescribeResponse{
		SupportedProviders: []string{providerID, pluginID},
		DisplayName:        "OpenCode Go Allowance",
		SupportsReset:      true,
	})
}

func handleQuotaFetch(raw []byte) ([]byte, error) {
	var req rpcQuotaFetchRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}
	now := time.Now().UTC()
	planLabel, cap5h, cap7d, cap30d := resolveAllowanceCapsForAuth(req)
	used5h, used7d, used30d, oldest5h, oldest7d, oldest30d := computeWindowTotals(req.AuthID, now)

	makeBucket := func(window string, used, capVal float64, oldest time.Time, dur time.Duration) pluginapi.QuotaBucket {
		rem := 1.0
		if capVal > 0 {
			rem = 1.0 - (used / capVal)
		}
		if rem < 0 {
			rem = 0
		} else if rem > 1 {
			rem = 1
		}
		resetAt := now.Add(dur)
		if !oldest.IsZero() {
			resetAt = oldest.Add(dur)
		}
		return pluginapi.QuotaBucket{
			Window:            window,
			RemainingFraction: math.Round(rem*10000) / 10000,
			ResetTime:         resetAt.Format(time.RFC3339),
			Description:       fmt.Sprintf("$%.4f / $%.2f allowance used (%.1f%% remaining)", used, capVal, rem*100),
		}
	}

	tierID := "go-plus"
	if strings.Contains(strings.ToLower(planLabel), "($10") {
		tierID = "go"
	}

	return okEnvelope(pluginapi.QuotaFetchResponse{
		Subscription: &pluginapi.QuotaSubscription{
			Plan:     planLabel,
			TierName: planLabel,
			TierID:   tierID,
		},
		Groups: []pluginapi.QuotaGroup{
			{
				DisplayName: "OpenCode Go Shared Allowance (Tracked by CPA)",
				Buckets: []pluginapi.QuotaBucket{
					makeBucket("5h", used5h, cap5h, oldest5h, rolling5hWindow),
					makeBucket("weekly", used7d, cap7d, oldest7d, rollingWeeklyWindow),
					makeBucket("monthly", used30d, cap30d, oldest30d, rollingMonthWindow),
				},
			},
		},
	})
}

func handleQuotaReset(_ []byte) ([]byte, error) {
	usageMu.Lock()
	usageEvents = nil
	usageMu.Unlock()
	return okEnvelope(pluginapi.QuotaResetResponse{
		Success: true,
		Message: "Cleared tracked OpenCode Go usage counters",
	})
}

func firstPositiveInt64(vals ...int64) int64 {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}
