package jsonl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func TestExtractAssistantEntries(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  []AssistantEntry
	}{
		{
			name: "claude code thinking and text blocks",
			lines: []string{
				`{"uuid":"u1","type":"assistant","message":{"role":"assistant","content":[` +
					`{"type":"thinking","thinking":"Check the config first."},` +
					`{"type":"text","text":"Reading the config."},` +
					`{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]}}`,
			},
			want: []AssistantEntry{
				{UUID: "u1", Index: 0, Kind: KindReasoning, Text: "Check the config first."},
				{UUID: "u1", Index: 1, Kind: KindAssistant, Text: "Reading the config."},
			},
		},
		{
			name: "claude code string content",
			lines: []string{
				`{"uuid":"u2","type":"assistant","message":{"role":"assistant","content":"All done."}}`,
			},
			want: []AssistantEntry{
				{UUID: "u2", Index: 0, Kind: KindAssistant, Text: "All done."},
			},
		},
		{
			name: "user turns and tool results are ignored",
			lines: []string{
				`{"uuid":"u3","type":"user","message":{"role":"user","content":"fix the assistant bug"}}`,
				`{"uuid":"u4","type":"user","message":{"role":"user","content":[` +
					`{"type":"tool_result","tool_use_id":"toolu_1","content":"assistant"}]}}`,
			},
			want: nil,
		},
		{
			name: "meta records are ignored",
			lines: []string{
				`{"uuid":"u5","type":"assistant","isMeta":true,"message":{"role":"assistant","content":[` +
					`{"type":"text","text":"caveat text"}]}}`,
			},
			want: nil,
		},
		{
			name: "codex response items",
			lines: []string{
				`{"type":"response_item","payload":{"type":"reasoning","summary":[` +
					`{"type":"summary_text","text":"**Planning the edit**"}]}}`,
				`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[` +
					`{"type":"output_text","text":"Edit applied."}]}}`,
			},
			want: []AssistantEntry{
				{UUID: "", Index: 0, Kind: KindReasoning, Text: "**Planning the edit**"},
				{UUID: "", Index: 0, Kind: KindAssistant, Text: "Edit applied."},
			},
		},
		{
			name: "reasoning item text blocks stay reasoning",
			lines: []string{
				`{"uuid":"u6","type":"reasoning","content":[{"type":"text","text":"weighing options"}]}`,
			},
			want: []AssistantEntry{
				{UUID: "u6", Index: 0, Kind: KindReasoning, Text: "weighing options"},
			},
		},
		{
			name: "blank and unsupported blocks are skipped",
			lines: []string{
				`{"uuid":"u7","type":"assistant","message":{"role":"assistant","content":[` +
					`{"type":"text","text":"   "},` +
					`{"type":"redacted_thinking","data":"opaque"},` +
					`{"type":"text","text":"kept"}]}}`,
			},
			want: []AssistantEntry{
				{UUID: "u7", Index: 2, Kind: KindAssistant, Text: "kept"},
			},
		},
		{
			name: "malformed lines do not abort the scan",
			lines: []string{
				`{"uuid":"u8","type":"assistant","message":{"role":"assistant","conte`,
				``,
				`not json at all "assistant"`,
				`{"uuid":"u9","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"survived"}]}}`,
			},
			want: []AssistantEntry{
				{UUID: "u9", Index: 0, Kind: KindAssistant, Text: "survived"},
			},
		},
		{
			name: "content and summary blocks get distinct indexes",
			lines: []string{
				`{"uuid":"u10","type":"assistant","role":"assistant","content":[{"type":"text","text":"answer"}],` +
					`"summary":[{"type":"summary_text","text":"why"}]}`,
			},
			want: []AssistantEntry{
				{UUID: "u10", Index: 0, Kind: KindAssistant, Text: "answer"},
				{UUID: "u10", Index: 1, Kind: KindReasoning, Text: "why"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractAssistantEntries(writeTranscript(t, tt.lines...))
			if err != nil {
				t.Fatalf("ExtractAssistantEntries() error = %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d entries, want %d: %+v", len(got), len(tt.want), got)
			}
			for i, want := range tt.want {
				if got[i] != want {
					t.Errorf("entry %d = %+v, want %+v", i, got[i], want)
				}
			}
		})
	}
}

// Transcript lines routinely exceed bufio.Scanner's 64KiB default because tool
// results are inlined, so an oversized line must not silently drop its text.
func TestExtractAssistantEntries_LargeLine(t *testing.T) {
	text := strings.Repeat("a", 200_000)
	path := writeTranscript(t,
		`{"uuid":"big","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"`+text+`"}]}}`)

	got, err := ExtractAssistantEntries(path)
	if err != nil {
		t.Fatalf("ExtractAssistantEntries() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].Text != text {
		t.Errorf("text length = %d, want %d", len(got[0].Text), len(text))
	}
}

func TestExtractAssistantEntries_MissingFile(t *testing.T) {
	_, err := ExtractAssistantEntries(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err == nil {
		t.Fatal("expected an error for a missing transcript")
	}
}
