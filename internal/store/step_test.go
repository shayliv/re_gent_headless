package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUsage_Add(t *testing.T) {
	a := Usage{InputTokens: 1, OutputTokens: 2, CacheCreationTokens: 3, CacheReadTokens: 4, APICalls: 5, Subagents: 6}
	b := Usage{InputTokens: 10, OutputTokens: 20, CacheCreationTokens: 30, CacheReadTokens: 40, APICalls: 50, Subagents: 60}

	want := Usage{InputTokens: 11, OutputTokens: 22, CacheCreationTokens: 33, CacheReadTokens: 44, APICalls: 55, Subagents: 66}
	if got := a.Add(b); got != want {
		t.Fatalf("Add = %+v, want %+v", got, want)
	}
}

func TestUsage_SubClampsAtZero(t *testing.T) {
	tests := []struct {
		name     string
		current  Usage
		baseline Usage
		want     Usage
	}{
		{
			name:     "growing transcript yields the difference",
			current:  Usage{InputTokens: 10, OutputTokens: 20, CacheCreationTokens: 30, CacheReadTokens: 40, APICalls: 5, Subagents: 2},
			baseline: Usage{InputTokens: 4, OutputTokens: 5, CacheCreationTokens: 6, CacheReadTokens: 7, APICalls: 2, Subagents: 1},
			want:     Usage{InputTokens: 6, OutputTokens: 15, CacheCreationTokens: 24, CacheReadTokens: 33, APICalls: 3, Subagents: 1},
		},
		{
			name:     "no baseline yields the whole reading",
			current:  Usage{InputTokens: 10, APICalls: 1},
			baseline: Usage{},
			want:     Usage{InputTokens: 10, APICalls: 1},
		},
		{
			name:     "a reset transcript never reports negative usage",
			current:  Usage{InputTokens: 1, OutputTokens: 1, APICalls: 1},
			baseline: Usage{InputTokens: 500, OutputTokens: 500, APICalls: 50},
			want:     Usage{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.current.Sub(tc.baseline); got != tc.want {
				t.Fatalf("Sub = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestUsage_IsZeroAndTotalTokens(t *testing.T) {
	if !(Usage{}).IsZero() {
		t.Fatal("the zero value must report IsZero")
	}
	if (Usage{Subagents: 1}).IsZero() {
		t.Fatal("a non-zero counter must not report IsZero")
	}

	usage := Usage{InputTokens: 1, OutputTokens: 2, CacheCreationTokens: 3, CacheReadTokens: 4, APICalls: 99}
	if got := usage.TotalTokens(); got != 10 {
		t.Fatalf("TotalTokens = %d, want 10 (API calls are not tokens)", got)
	}
}

// Usage is optional on the wire: steps captured before it existed must still
// decode, and steps captured without a transcript must not grow new JSON keys
// (which would change their content hash).
func TestStep_UsageIsOptionalOnTheWire(t *testing.T) {
	legacy := []byte(`{"tree":"abc","session_id":"claude_code--s","ts":1700000000000000000}`)
	var step Step
	if err := json.Unmarshal(legacy, &step); err != nil {
		t.Fatalf("decode legacy step: %v", err)
	}
	if step.Usage != nil || step.UsageTotal != nil {
		t.Fatalf("legacy step gained usage: %+v / %+v", step.Usage, step.UsageTotal)
	}

	encoded, err := json.Marshal(&Step{Tree: "abc", SessionID: "claude_code--s", TimestampNanos: 1700000000000000000})
	if err != nil {
		t.Fatalf("encode step: %v", err)
	}
	if strings.Contains(string(encoded), "usage") {
		t.Fatalf("a step without usage must not serialize usage keys: %s", encoded)
	}
}

func TestStep_UsageRoundTrips(t *testing.T) {
	usage := Usage{InputTokens: 3, OutputTokens: 114, CacheCreationTokens: 19576, CacheReadTokens: 16601, APICalls: 2, Subagents: 1}
	total := usage.Add(usage)

	encoded, err := json.Marshal(&Step{Tree: "abc", SessionID: "s", Usage: &usage, UsageTotal: &total})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	var decoded Step
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Usage == nil || *decoded.Usage != usage {
		t.Fatalf("usage = %+v, want %+v", decoded.Usage, usage)
	}
	if decoded.UsageTotal == nil || *decoded.UsageTotal != total {
		t.Fatalf("usage total = %+v, want %+v", decoded.UsageTotal, total)
	}
}

func TestNormalizeCauses_UsesCausesAsCanonicalSource(t *testing.T) {
	step := &Step{
		Cause:  Cause{ToolName: "Stale", ToolUseID: "old"},
		Causes: []Cause{{ToolName: "Write", ToolUseID: "new"}},
	}

	step.NormalizeCauses()

	if step.Cause.ToolName != "Write" || step.Cause.ToolUseID != "new" {
		t.Fatalf("legacy cause was not canonicalized from Causes: %#v", step.Cause)
	}
}
