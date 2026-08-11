package classifier

import "testing"

func TestClassify_KnownRouting(t *testing.T) {
	cases := []struct {
		taskType string
		text     string
		want     string
	}{
		{"classify_email", "short newsletter blurb", "ollama"},
		{"draft_reply", "hi there", "claude"},
		{"summarize_thread", "short thread", "ollama"},
		{"extract_health_metrics", "bp 120/80", "ollama"},
		{"summarize_health_trends", "trend text", "claude"},
		{"flag_health_anomaly", "anomaly text", "claude"},
	}

	for _, c := range cases {
		got := Classify(c.taskType, c.text)
		if got != c.want {
			t.Errorf("Classify(%q, %q) = %q, want %q", c.taskType, c.text, got, c.want)
		}
	}
}

func TestClassify_UnknownTaskDefaultsToOllama(t *testing.T) {
	got := Classify("some_unregistered_task", "plain text")
	if got != "ollama" {
		t.Errorf("expected unknown task type to default to ollama, got %q", got)
	}
}

func TestClassify_LongPayloadEscalatesToClaude(t *testing.T) {
	longText := make([]byte, SizeEscalationThresholdChars+1)
	for i := range longText {
		longText[i] = 'a'
	}
	got := Classify("classify_email", string(longText))
	if got != "claude" {
		t.Errorf("expected long payload to escalate to claude, got %q", got)
	}
}

func TestClassify_ComplexKeywordEscalatesToClaude(t *testing.T) {
	for _, kw := range ComplexKeywords {
		got := Classify("classify_email", "please "+kw+" this for me")
		if got != "claude" {
			t.Errorf("expected keyword %q to escalate to claude, got %q", kw, got)
		}
	}
}

func TestLooksLowConfidence(t *testing.T) {
	cases := []struct {
		response string
		want     bool
	}{
		{"", true},
		{"ok", true},
		{"I'm not sure about this one", true},
		{"I don't know", true},
		{"This is unclear to me", true},
		{"Cannot determine the category", true},
		{"newsletter", false},
		{"This email is a receipt from an online store.", false},
	}

	for _, c := range cases {
		got := LooksLowConfidence(c.response)
		if got != c.want {
			t.Errorf("LooksLowConfidence(%q) = %v, want %v", c.response, got, c.want)
		}
	}
}
