package jsonl

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Kinds of assistant-authored blocks returned by ExtractAssistantEntries.
const (
	KindAssistant = "assistant"
	KindReasoning = "reasoning"
)

// maxTranscriptLineBytes bounds a single JSONL line. Transcript lines routinely
// exceed bufio.Scanner's 64KiB default because tool results are inlined.
const maxTranscriptLineBytes = 16 << 20

// AssistantEntry is one assistant-authored text or reasoning block pulled out of
// a host transcript. UUID and Index identify the block within the transcript;
// together with the text they give an entry a stable identity across re-scans.
type AssistantEntry struct {
	UUID  string // host message id, when the transcript carries one
	Index int    // position of the block within its message
	Kind  string // KindAssistant or KindReasoning
	Text  string
}

// ExtractAssistantEntries scans a host transcript and returns every assistant
// text and reasoning block in file order.
//
// The scan is deliberately forgiving: unparseable, truncated, or unrecognised
// lines are skipped instead of failing the read, because hooks run inside a live
// agent turn and the transcript may be mid-write or rewritten by /compact. A
// non-nil error may be returned alongside a non-empty slice; callers should keep
// the entries and log the error.
func ExtractAssistantEntries(path string) ([]AssistantEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer func() { _ = f.Close() }()

	return parseAssistantEntries(f)
}

// assistantHints are cheap substring probes that let us skip JSON parsing for
// the majority of transcript lines (user turns, tool results, meta records).
var assistantHints = [][]byte{
	[]byte(`"assistant"`),
	[]byte(`"thinking"`),
	[]byte(`"reasoning"`),
}

func parseAssistantEntries(r io.Reader) ([]AssistantEntry, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxTranscriptLineBytes)

	var entries []AssistantEntry
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || !hasAssistantHint(line) {
			continue
		}

		var envelope transcriptEnvelope
		if err := json.Unmarshal(line, &envelope); err != nil {
			continue
		}
		if envelope.IsMeta {
			continue
		}

		body, ok := envelope.assistantBody()
		if !ok {
			continue
		}
		entries = appendBodyEntries(entries, envelope.entryUUID(body), body)
	}

	if err := scanner.Err(); err != nil {
		return entries, fmt.Errorf("scan transcript: %w", err)
	}
	return entries, nil
}

func hasAssistantHint(line []byte) bool {
	for _, hint := range assistantHints {
		if bytes.Contains(line, hint) {
			return true
		}
	}
	return false
}

// transcriptEnvelope covers the record shapes re_gent's hosts emit: Claude Code
// wraps the API message under "message", Codex wraps response items under
// "payload", and some hosts write the message fields at the top level.
type transcriptEnvelope struct {
	UUID    string          `json:"uuid"`
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	IsMeta  bool            `json:"isMeta"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Summary json.RawMessage `json:"summary"`
	Message *messageBody    `json:"message"`
	Payload *messageBody    `json:"payload"`
}

type messageBody struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Summary json.RawMessage `json:"summary"`
}

// assistantBody resolves the envelope to the message body that holds content
// blocks, and reports whether that body was authored by the assistant.
func (e transcriptEnvelope) assistantBody() (messageBody, bool) {
	switch {
	case e.Message != nil:
		body := *e.Message
		if body.Role == "" {
			body.Role = e.Role
		}
		return body, isAssistantAuthored(e.Type, body)
	case e.Payload != nil:
		return *e.Payload, isAssistantAuthored(e.Type, *e.Payload)
	default:
		body := messageBody{ID: e.ID, Type: e.Type, Role: e.Role, Content: e.Content, Summary: e.Summary}
		return body, isAssistantAuthored(e.Type, body)
	}
}

func isAssistantAuthored(envelopeType string, body messageBody) bool {
	if body.Role != "" {
		return body.Role == "assistant"
	}
	return envelopeType == KindAssistant || envelopeType == KindReasoning || body.Type == KindReasoning
}

func (e transcriptEnvelope) entryUUID(body messageBody) string {
	for _, candidate := range []string{e.UUID, body.ID, e.ID} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func appendBodyEntries(entries []AssistantEntry, uuid string, body messageBody) []AssistantEntry {
	// A reasoning item's blocks are reasoning even when they are typed "text".
	forcedKind := ""
	if body.Type == KindReasoning {
		forcedKind = KindReasoning
	}

	index := 0
	entries, index = appendContentEntries(entries, uuid, index, body.Content, forcedKind)
	entries, _ = appendContentEntries(entries, uuid, index, body.Summary, KindReasoning)
	return entries
}

// appendContentEntries handles both string content and content-block arrays. The
// returned index is the next free block position, so content and summary blocks
// of one message never collide.
func appendContentEntries(entries []AssistantEntry, uuid string, index int, content json.RawMessage, forcedKind string) ([]AssistantEntry, int) {
	if len(content) == 0 {
		return entries, index
	}

	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		kind := forcedKind
		if kind == "" {
			kind = KindAssistant
		}
		return appendEntry(entries, uuid, index, kind, text), index + 1
	}

	var blocks []map[string]interface{}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return entries, index
	}

	for _, block := range blocks {
		kind, text := blockKindAndText(block, forcedKind)
		if kind != "" {
			entries = appendEntry(entries, uuid, index, kind, text)
		}
		index++
	}
	return entries, index
}

// blockKindAndText maps a content block to an entry kind. An empty kind means
// the block carries no assistant prose (tool_use, tool_result, images, redacted
// thinking) and should be ignored.
func blockKindAndText(block map[string]interface{}, forcedKind string) (string, string) {
	blockType, _ := block["type"].(string)

	switch blockType {
	case "text", "output_text":
		kind := forcedKind
		if kind == "" {
			kind = KindAssistant
		}
		return kind, stringField(block, "text")
	case "thinking":
		return KindReasoning, firstNonEmpty(stringField(block, "thinking"), stringField(block, "text"))
	case "reasoning", "reasoning_text", "summary_text":
		return KindReasoning, firstNonEmpty(stringField(block, "text"), stringField(block, "summary"), stringField(block, "reasoning"))
	default:
		return "", ""
	}
}

func appendEntry(entries []AssistantEntry, uuid string, index int, kind, text string) []AssistantEntry {
	if strings.TrimSpace(text) == "" {
		return entries
	}
	return append(entries, AssistantEntry{UUID: uuid, Index: index, Kind: kind, Text: text})
}

func stringField(block map[string]interface{}, key string) string {
	value, _ := block[key].(string)
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
