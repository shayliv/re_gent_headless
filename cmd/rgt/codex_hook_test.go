package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regent-vcs/regent/internal/index"
	"github.com/regent-vcs/regent/internal/store"
)

func TestRunCodexHook_CapturesTurn(t *testing.T) {
	root := t.TempDir()
	if _, err := store.Init(root); err != nil {
		t.Fatalf("init store: %v", err)
	}

	runCodexPayload(t, fmt.Sprintf(`{"hook_event_name":"session_start","session_id":"codex-session","cwd":%q,"model":"gpt-5.5"}`, root))
	runCodexPayload(t, fmt.Sprintf(`{"hook_event_name":"user-prompt-submit","session_id":"codex-session","turn_id":"turn-1","cwd":%q,"prompt":"write file"}`, root))
	if err := os.WriteFile(filepath.Join(root, "codex.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runCodexPayload(t, fmt.Sprintf(`{"hook_event_name":"PostToolUse","session_id":"codex-session","turn_id":"turn-1","cwd":%q,"tool_name":"Write","tool_use_id":"tool-1","tool_input":{"file_path":"codex.txt","content":"ok\n"},"tool_response":{"ok":true}}`, root))
	runCodexPayload(t, fmt.Sprintf(`{"hook_event_name":"stop","session_id":"codex-session","turn_id":"turn-1","cwd":%q,"last_assistant_message":"done"}`, root))

	s, err := store.Open(filepath.Join(root, ".regent"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	idx, err := index.Open(s)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer func() { _ = idx.Close() }()

	sessionID := "codex_cli--" + url.PathEscape("codex-session")
	steps, err := idx.ListSteps(sessionID, 10)
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("expected one step, got %d", len(steps))
	}
	if steps[0].Origin != "codex_cli" || steps[0].TurnID != "turn-1" || steps[0].ToolName != "Write" {
		t.Fatalf("unexpected step metadata: %#v", steps[0])
	}

	messages, err := idx.GetMessagesForStep(steps[0].Hash)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("expected 4 linked messages, got %d", len(messages))
	}
}

func TestRunCodexHook_UnsupportedEventLogsAndDoesNotFail(t *testing.T) {
	root := t.TempDir()
	if _, err := store.Init(root); err != nil {
		t.Fatalf("init store: %v", err)
	}

	runCodexPayload(t, fmt.Sprintf(`{"hook_event_name":"unknown_event","session_id":"codex-session","cwd":%q}`, root))

	data, err := os.ReadFile(filepath.Join(root, ".regent", "log", "hook-error.log"))
	if err != nil {
		t.Fatalf("expected hook error log: %v", err)
	}
	if !strings.Contains(string(data), "unsupported Codex hook event") {
		t.Fatalf("unexpected hook error log: %s", data)
	}
}

func TestRunCodexHook_NoStoreIsNoop(t *testing.T) {
	root := t.TempDir()

	runCodexPayload(t, fmt.Sprintf(`{"hook_event_name":"session_start","session_id":"codex-session","cwd":%q}`, root))

	if _, err := os.Stat(filepath.Join(root, ".regent")); !os.IsNotExist(err) {
		t.Fatalf("hook should not create .regent, stat err=%v", err)
	}
}

func TestRunCodexHook_InvalidPayloadNoStoreIsNoop(t *testing.T) {
	root := t.TempDir()
	chdir(t, root)

	runCodexPayload(t, `{not-json`)

	if _, err := os.Stat(filepath.Join(root, ".regent")); !os.IsNotExist(err) {
		t.Fatalf("invalid hook payload should not create .regent, stat err=%v", err)
	}
}

func TestRunCodexHook_InvalidPayloadLogsWhenStoreExists(t *testing.T) {
	root := t.TempDir()
	chdir(t, root)
	if _, err := store.Init(root); err != nil {
		t.Fatalf("init store: %v", err)
	}

	runCodexPayload(t, `{not-json`)

	data, err := os.ReadFile(filepath.Join(root, ".regent", "log", "hook-error.log"))
	if err != nil {
		t.Fatalf("expected hook error log: %v", err)
	}
	if !strings.Contains(string(data), "parse payload") {
		t.Fatalf("unexpected hook error log: %s", data)
	}
}

func TestRunToolBatchHook_MarshalsStringResponseAsJSON(t *testing.T) {
	root := t.TempDir()
	if _, err := store.Init(root); err != nil {
		t.Fatalf("init store: %v", err)
	}

	runToolBatchPayload(t, fmt.Sprintf(`{"session_id":"claude-session","cwd":%q,"tool_calls":[{"tool_name":"Bash","tool_use_id":"tool-1","tool_input":{"command":"printf ok"},"tool_response":"ok"}]}`, root))

	s, err := store.Open(filepath.Join(root, ".regent"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	idx, err := index.Open(s)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer func() { _ = idx.Close() }()

	sessionID := "claude_code--" + url.PathEscape("claude-session")
	messages, err := idx.GetAllPendingMessages(sessionID)
	if err != nil {
		t.Fatalf("pending messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected call/result messages, got %d", len(messages))
	}

	resultData, err := s.ReadBlob(store.Hash(messages[1].ToolOutput))
	if err != nil {
		t.Fatalf("read tool output: %v", err)
	}
	if string(resultData) != `"ok"` {
		t.Fatalf("tool response blob = %s, want JSON string", resultData)
	}
}

func TestRunToolBatchHook_PreservesStructuredResponse(t *testing.T) {
	root := t.TempDir()
	if _, err := store.Init(root); err != nil {
		t.Fatalf("init store: %v", err)
	}

	runToolBatchPayload(t, fmt.Sprintf(`{"session_id":"claude-session","cwd":%q,"tool_calls":[{"tool_name":"Read","tool_use_id":"tool-1","tool_input":{"file_path":"notes.txt"},"tool_response":[{"type":"text","text":"ok"}]}]}`, root))

	s, err := store.Open(filepath.Join(root, ".regent"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	idx, err := index.Open(s)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer func() { _ = idx.Close() }()

	sessionID := "claude_code--" + url.PathEscape("claude-session")
	messages, err := idx.GetAllPendingMessages(sessionID)
	if err != nil {
		t.Fatalf("pending messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected call/result messages, got %d", len(messages))
	}

	resultData, err := s.ReadBlob(store.Hash(messages[1].ToolOutput))
	if err != nil {
		t.Fatalf("read tool output: %v", err)
	}
	if string(resultData) != `[{"type":"text","text":"ok"}]` {
		t.Fatalf("tool response blob = %s", resultData)
	}
}

func runCodexPayload(t *testing.T, payload string) {
	t.Helper()
	runWithStdin(t, payload, func() error {
		return runCodexHook(nil, nil)
	})
}

func runToolBatchPayload(t *testing.T, payload string) {
	t.Helper()
	runWithStdin(t, payload, func() error {
		return runToolBatchHook(nil, nil)
	})
}

func runMessageHookPayload(t *testing.T, direction string, payload string) {
	t.Helper()
	runWithStdin(t, payload, func() error {
		return runMessageHook(nil, []string{direction})
	})
}

// TestAgentIDFromPayloadOrEnv covers the two sources of agent_id for Claude Code subagents:
// the JSON payload field and the CLAUDE_AGENT_ID env var set by the host process.
func TestAgentIDFromPayloadOrEnv(t *testing.T) {
	t.Run("payload takes precedence over env var", func(t *testing.T) {
		t.Setenv("CLAUDE_AGENT_ID", "env-agent")
		if got := agentIDFromPayloadOrEnv("payload-agent"); got != "payload-agent" {
			t.Errorf("got %q, want payload-agent", got)
		}
	})
	t.Run("falls back to CLAUDE_AGENT_ID when payload is empty", func(t *testing.T) {
		t.Setenv("CLAUDE_AGENT_ID", "env-agent-xyz")
		if got := agentIDFromPayloadOrEnv(""); got != "env-agent-xyz" {
			t.Errorf("got %q, want env-agent-xyz", got)
		}
	})
	t.Run("returns empty string when both are unset", func(t *testing.T) {
		t.Setenv("CLAUDE_AGENT_ID", "")
		if got := agentIDFromPayloadOrEnv(""); got != "" {
			t.Errorf("got %q, want empty string", got)
		}
	})
}

// TestRunMessageHook_SubagentAgentIDCaptured verifies that a Task-spawned subagent's
// agent_id in the hook payload flows through to the stored step and object store.
func TestRunMessageHook_SubagentAgentIDCaptured(t *testing.T) {
	root := t.TempDir()
	if _, err := store.Init(root); err != nil {
		t.Fatalf("init store: %v", err)
	}

	agentID := "agent_subagent_abc123"

	runMessageHookPayload(t, "user", fmt.Sprintf(`{"session_id":"sub-ses","cwd":%q,"turn_id":"turn-1","prompt":"do task","agent_id":%q}`, root, agentID))
	if err := os.WriteFile(filepath.Join(root, "sub.txt"), []byte("sub\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runToolBatchPayload(t, fmt.Sprintf(`{"session_id":"sub-ses","cwd":%q,"turn_id":"turn-1","agent_id":%q,"tool_calls":[{"tool_name":"Write","tool_use_id":"tool-sub-1","tool_input":{"file_path":"sub.txt","content":"sub\n"},"tool_response":{"ok":true}}]}`, root, agentID))
	runMessageHookPayload(t, "assistant", fmt.Sprintf(`{"session_id":"sub-ses","cwd":%q,"turn_id":"turn-1","last_assistant_message":"done","agent_id":%q}`, root, agentID))

	s, err := store.Open(filepath.Join(root, ".regent"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	idx, err := index.Open(s)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer func() { _ = idx.Close() }()

	sessionID := "claude_code--" + url.PathEscape("sub-ses")
	steps, err := idx.ListSteps(sessionID, 10)
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(steps))
	}
	if steps[0].AgentID != agentID {
		t.Errorf("index step AgentID = %q, want %q", steps[0].AgentID, agentID)
	}

	stepObj, err := s.ReadStep(steps[0].Hash)
	if err != nil {
		t.Fatalf("read step object: %v", err)
	}
	if stepObj.AgentID != agentID {
		t.Errorf("step object AgentID = %q, want %q", stepObj.AgentID, agentID)
	}
}

// TestRunMessageHook_SubagentAgentIDFromEnv verifies the CLAUDE_AGENT_ID env-var
// fallback: when agent_id is absent from the payload (as in early Claude Code builds)
// the env var set by the host process is used instead.
func TestRunMessageHook_SubagentAgentIDFromEnv(t *testing.T) {
	root := t.TempDir()
	if _, err := store.Init(root); err != nil {
		t.Fatalf("init store: %v", err)
	}

	t.Setenv("CLAUDE_AGENT_ID", "env_agent_999")

	// No agent_id field in any payload — falls back to CLAUDE_AGENT_ID.
	runMessageHookPayload(t, "user", fmt.Sprintf(`{"session_id":"env-ses","cwd":%q,"turn_id":"turn-env","prompt":"env task"}`, root))
	if err := os.WriteFile(filepath.Join(root, "env.txt"), []byte("env\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runToolBatchPayload(t, fmt.Sprintf(`{"session_id":"env-ses","cwd":%q,"turn_id":"turn-env","tool_calls":[{"tool_name":"Write","tool_use_id":"tool-env-1","tool_input":{"file_path":"env.txt","content":"env\n"},"tool_response":{"ok":true}}]}`, root))
	runMessageHookPayload(t, "assistant", fmt.Sprintf(`{"session_id":"env-ses","cwd":%q,"turn_id":"turn-env","last_assistant_message":"env done"}`, root))

	s, err := store.Open(filepath.Join(root, ".regent"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	idx, err := index.Open(s)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer func() { _ = idx.Close() }()

	sessionID := "claude_code--" + url.PathEscape("env-ses")
	steps, err := idx.ListSteps(sessionID, 10)
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(steps))
	}
	if steps[0].AgentID != "env_agent_999" {
		t.Errorf("index step AgentID = %q, want env_agent_999", steps[0].AgentID)
	}
}

func runWithStdin(t *testing.T, payload string, fn func() error) {
	t.Helper()

	oldStdin := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := io.WriteString(writer, payload); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	os.Stdin = reader
	defer func() {
		os.Stdin = oldStdin
		_ = reader.Close()
	}()

	if err := fn(); err != nil {
		t.Fatalf("hook returned error: %v", err)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()

	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldDir); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})
}
