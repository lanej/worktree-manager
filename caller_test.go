package main

import "testing"

func setCallerEnvironment(t *testing.T, values map[string]string) {
	t.Helper()

	for _, name := range []string{
		"CODEX_THREAD_ID", "CODEX_SESSION_ID", "CLAUDE_CODE_SESSION_ID", "CLAUDECODE",
		"PI_SESSION_ID", "PI_CODING_AGENT", "GEMINI_CLI", "OPENCODE_SESSION_ID", "OPENCODE",
		"AI_AGENT", "WORKTREE_AGENT", "WORKTREE_OWNER_ID",
	} {
		t.Setenv(name, values[name])
	}
}

func TestCallerEnvironment(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, agent, owner string
		env                map[string]string
		detected           bool
	}{
		{name: "manual", agent: "manual"},
		{name: "codex thread", env: map[string]string{"CODEX_THREAD_ID": "thread", "CODEX_SESSION_ID": "session"}, agent: "codex", owner: "thread", detected: true},
		{name: "codex session", env: map[string]string{"CODEX_SESSION_ID": "session"}, agent: "codex", owner: "session", detected: true},
		{name: "claude session", env: map[string]string{"CLAUDE_CODE_SESSION_ID": "claude-session"}, agent: "claude", owner: "claude-session", detected: true},
		{name: "claude marker", env: map[string]string{"CLAUDECODE": "1"}, agent: "claude", detected: true},
		{name: "pi session", env: map[string]string{"PI_SESSION_ID": "pi-session"}, agent: "pi", owner: "pi-session", detected: true},
		{name: "pi marker", env: map[string]string{"PI_CODING_AGENT": "true"}, agent: "pi", detected: true},
		{name: "gemini marker", env: map[string]string{"GEMINI_CLI": "1"}, agent: "gemini", detected: true},
		{name: "opencode session", env: map[string]string{"OPENCODE_SESSION_ID": "opencode-session"}, agent: "opencode", owner: "opencode-session", detected: true},
		{name: "opencode marker", env: map[string]string{"OPENCODE": "1"}, agent: "opencode", detected: true},
		{name: "generic marker", env: map[string]string{"AI_AGENT": "custom"}, agent: "custom", detected: true},
		{name: "matching generic marker", env: map[string]string{"AI_AGENT": "pi", "PI_SESSION_ID": "pi-session"}, agent: "pi", owner: "pi-session", detected: true},
		{name: "conflicting clients", env: map[string]string{"CODEX_THREAD_ID": "thread", "CLAUDE_CODE_SESSION_ID": "claude-session"}, detected: true},
		{name: "native over generic marker", env: map[string]string{"AI_AGENT": "pi", "OPENCODE": "1"}, agent: "opencode", detected: true},
		{name: "claude versioned marker", env: map[string]string{"AI_AGENT": "claude-code_2-1-295_agent", "CLAUDECODE": "1", "CLAUDE_CODE_SESSION_ID": "session"}, agent: "claude", owner: "session", detected: true},
		{name: "custom override", env: map[string]string{"WORKTREE_AGENT": "custom", "WORKTREE_OWNER_ID": "launch-token", "CODEX_THREAD_ID": "thread"}, agent: "custom", owner: "launch-token", detected: true},
		{name: "native override", env: map[string]string{"WORKTREE_AGENT": "claude", "CLAUDE_CODE_SESSION_ID": "claude-session", "CODEX_THREAD_ID": "thread"}, agent: "claude", owner: "claude-session", detected: true},
		{name: "owner override", env: map[string]string{"WORKTREE_AGENT": "claude", "WORKTREE_OWNER_ID": "launch-token", "CLAUDE_CODE_SESSION_ID": "claude-session"}, agent: "claude", owner: "launch-token", detected: true},
		{name: "unpaired owner", env: map[string]string{"WORKTREE_OWNER_ID": "launch-token"}, agent: "manual"},
		{name: "disabled markers", env: map[string]string{"CLAUDECODE": "0", "PI_CODING_AGENT": "false", "GEMINI_CLI": "0", "OPENCODE": "0"}, agent: "manual"},
		{name: "configuration is not identity", env: map[string]string{"CLAUDE_CONFIG_DIR": "/config", "GEMINI_MODEL": "model", "OPENCODE_CONFIG": "/config", "PI_CODING_AGENT_DIR": "/config"}, agent: "manual"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			caller := callerFromEnvironment(func(name string) string { return test.env[name] })
			if caller.agent != test.agent || caller.detected != test.detected || caller.owners[test.agent] != test.owner {
				t.Fatalf("unexpected caller: %+v", caller)
			}
		})
	}
}
