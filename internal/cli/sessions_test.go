package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/regent-vcs/regent/internal/index"
	"github.com/regent-vcs/regent/internal/store"
)

func TestSessionsCmdJSONFormat(t *testing.T) {
	root := t.TempDir()
	withWorkingDir(t, root)

	s, err := store.Init(root)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}

	idx, err := index.Open(s)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}

	sessionID := "claude-20260502-143021"
	agentID := "claude-code"
	firstStep := writeIndexedSessionStep(t, s, idx, sessionID, "", agentID, time.Date(2026, 5, 2, 14, 30, 21, 0, time.UTC))
	writeIndexedSessionStep(t, s, idx, sessionID, firstStep, agentID, time.Date(2026, 5, 2, 14, 31, 21, 0, time.UTC))

	if err := idx.Close(); err != nil {
		t.Fatalf("close index: %v", err)
	}

	cmd := SessionsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--format=json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute sessions command: %v", err)
	}

	var got struct {
		Sessions []struct {
			SessionID    string `json:"session_id"`
			StepCount    int    `json:"step_count"`
			LastActivity string `json:"last_activity"`
			AgentID      string `json:"agent_id"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out.String())
	}

	if len(got.Sessions) != 1 {
		t.Fatalf("sessions length = %d, want 1; output: %s", len(got.Sessions), out.String())
	}

	session := got.Sessions[0]
	if session.SessionID != sessionID {
		t.Fatalf("session_id = %q, want %q", session.SessionID, sessionID)
	}
	if session.StepCount != 2 {
		t.Fatalf("step_count = %d, want 2", session.StepCount)
	}
	if session.AgentID != agentID {
		t.Fatalf("agent_id = %q, want %q", session.AgentID, agentID)
	}
	if _, err := time.Parse(time.RFC3339, session.LastActivity); err != nil {
		t.Fatalf("last_activity is not RFC3339: %q", session.LastActivity)
	}
}

func TestSessionsCmdHelpDocumentsFormatFlag(t *testing.T) {
	cmd := SessionsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute help: %v", err)
	}

	help := out.String()
	if !strings.Contains(help, "--format") {
		t.Fatalf("help does not document --format flag:\n%s", help)
	}
	if !strings.Contains(help, "text or json") {
		t.Fatalf("help does not document supported formats:\n%s", help)
	}
}

func withWorkingDir(t *testing.T, dir string) {
	t.Helper()

	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working dir: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldDir); err != nil {
			t.Fatalf("restore working dir: %v", err)
		}
	})
}

func writeIndexedSessionStep(t *testing.T, s *store.Store, idx *index.DB, sessionID string, parent store.Hash, agentID string, timestamp time.Time) store.Hash {
	t.Helper()

	blobHash, err := s.WriteBlob([]byte("content"))
	if err != nil {
		t.Fatalf("write blob: %v", err)
	}
	tree := &store.Tree{
		Entries: []store.TreeEntry{
			{Path: "file.txt", Blob: blobHash, Mode: 0o644},
		},
	}
	treeHash, err := s.WriteTree(tree)
	if err != nil {
		t.Fatalf("write tree: %v", err)
	}

	step := &store.Step{
		Parent:         parent,
		SessionID:      sessionID,
		Origin:         "claude_code",
		AgentID:        agentID,
		TimestampNanos: timestamp.UnixNano(),
		Tree:           treeHash,
		Cause: store.Cause{
			ToolName:  "Write",
			ToolUseID: "tool_1",
		},
	}

	stepHash, err := s.WriteStep(step)
	if err != nil {
		t.Fatalf("write step: %v", err)
	}
	if err := idx.IndexStep(stepHash, step, tree); err != nil {
		t.Fatalf("index step: %v", err)
	}

	return stepHash
}
