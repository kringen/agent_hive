package classifier

import "strings"

// TaskRouting mirrors classifier.py's TASK_ROUTING table. Agents set
// TaskType when publishing; this is the main lever for tuning cost vs.
// quality later without touching agent code.
var TaskRouting = map[string]string{
	// Email agent
	"classify_email":   "ollama",
	"draft_reply":      "claude",
	"summarize_thread": "ollama",

	// Health agent
	"extract_health_metrics":  "ollama",
	"summarize_health_trends": "claude",
	"flag_health_anomaly":     "claude",
}

// SizeEscalationThresholdChars: escalate to Claude if the payload text
// exceeds this — long context tends to degrade badly on small local models.
const SizeEscalationThresholdChars = 4000

// ComplexKeywords suggest a task needs better reasoning than a small
// local model reliably gives, even if not explicitly routed to Claude.
var ComplexKeywords = []string{"draft", "write a reply", "analyze", "why", "recommend", "anomaly"}

// Classify returns "ollama" or "claude" for a given task.
func Classify(taskType string, payloadText string) string {
	model, ok := TaskRouting[taskType]
	if !ok {
		model = "ollama" // unknown task types default cheap
	}

	if len(payloadText) > SizeEscalationThresholdChars {
		return "claude"
	}

	lower := strings.ToLower(payloadText)
	for _, kw := range ComplexKeywords {
		if strings.Contains(lower, kw) {
			return "claude"
		}
	}

	return model
}

// LooksLowConfidence is a cheap sanity check on a local model's output
// before trusting it. Ollama doesn't give a real confidence score — this
// just catches the common failure modes: empty output or an obvious hedge.
func LooksLowConfidence(response string) bool {
	trimmed := strings.TrimSpace(response)
	if len(trimmed) < 3 {
		return true
	}

	lower := strings.ToLower(trimmed)
	hedges := []string{"i'm not sure", "i don't know", "unclear", "cannot determine"}
	for _, h := range hedges {
		if strings.Contains(lower, h) {
			return true
		}
	}

	return false
}
