package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func allocationFixture(t *testing.T) (*policy, allocationOptions) {
	t.Helper()

	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	p := &policy{ctx: t.Context()}
	if _, err := p.workingGit(root, "init", "-q", "--initial-branch=main"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".worktrees/\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := p.workingGit(root, "add", ".gitignore"); err != nil {
		t.Fatal(err)
	}

	if _, err := p.workingGit(root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "initial"); err != nil {
		t.Fatal(err)
	}

	primary, err := canonical(root)
	if err != nil {
		t.Fatal(err)
	}

	return p, allocationOptions{repo: primary, primary: primary, common: filepath.Join(primary, ".git"), agent: "codex"}
}

func TestAllocationCollisions(t *testing.T) {
	for _, collision := range []string{"path", "branch", "descendant branch", "record"} {
		t.Run(collision, func(t *testing.T) {
			p, options := allocationFixture(t)
			first := "12345678" + strings.Repeat("0", 24)
			second := "87654321" + strings.Repeat("1", 24)

			switch collision {
			case "path":
				path := filepath.Join(options.primary, ".worktrees/codex-12345678")
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "branch":
				if _, err := p.workingGit(options.repo, "branch", "worktree/codex-12345678"); err != nil {
					t.Fatal(err)
				}
			case "descendant branch":
				if _, err := p.workingGit(options.repo, "branch", "worktree/codex-12345678/topic"); err != nil {
					t.Fatal(err)
				}
			case "record":
				if err := saveTask(task{ID: first, State: taskFinished, Common: gitText(options.common), Path: gitText(options.primary)}); err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatal("unknown collision fixture")
			}

			calls := 0

			allocation, err := p.allocate(options, func() (string, error) {
				calls++
				if calls == 1 {
					return first, nil
				}

				return second, nil
			})
			if err != nil {
				t.Fatal(err)
			}

			if calls != 2 || allocation.id != second || allocation.branch != "worktree/codex-87654321" || allocation.path != filepath.Join(options.primary, ".worktrees/codex-87654321") {
				t.Fatalf("collision did not select a fresh identity: %+v (%d attempts)", allocation, calls)
			}
		})
	}
}

func TestAllocationExhaustionAndRandomFailure(t *testing.T) {
	p, options := allocationFixture(t)
	if _, err := p.workingGit(options.repo, "branch", "worktree/codex-12345678"); err != nil {
		t.Fatal(err)
	}

	calls := 0

	_, err := p.allocate(options, func() (string, error) {
		calls++

		return "12345678" + strings.Repeat("0", 24), nil
	})
	if err == nil || calls != 16 {
		t.Fatalf("unbounded or accepted collisions: attempts=%d err=%v", calls, err)
	}

	randomErr := errors.New("random source unavailable")

	_, err = p.allocate(options, func() (string, error) { return "", randomErr })
	if !errors.Is(err, randomErr) {
		t.Fatalf("lost random-source error: %v", err)
	}
}

func TestCreateEnvironmentIdentity(t *testing.T) {
	for _, test := range []struct {
		name, threadID, sessionID, agent, ownerID string
		args                                      []string
	}{
		{name: "manual", agent: "manual"},
		{name: "thread", threadID: "full-thread-id", agent: "codex", ownerID: "full-thread-id"},
		{name: "session fallback", sessionID: "full-session-id", agent: "codex", ownerID: "full-session-id"},
		{name: "thread precedence", threadID: "thread", sessionID: "session", agent: "codex", ownerID: "thread"},
		{name: "explicit owner", threadID: "thread", args: []string{"--owner-id", "launcher"}, agent: "codex", ownerID: "launcher"},
		{name: "empty owner", threadID: "thread", args: []string{"--owner-id", ""}, agent: "codex"},
		{name: "explicit agent", threadID: "thread", args: []string{"--agent", "claude"}, agent: "claude"},
		{name: "explicit manual", threadID: "thread", args: []string{"--agent", "manual"}, agent: "manual"},
		{name: "explicit codex", threadID: "thread", args: []string{"--agent", "codex"}, agent: "codex", ownerID: "thread"},
		{name: "explicit pair", threadID: "thread", args: []string{"--agent", "pi", "--owner-id", "pi-owner"}, agent: "pi", ownerID: "pi-owner"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, options := allocationFixture(t)
			t.Setenv("CODEX_THREAD_ID", test.threadID)
			t.Setenv("CODEX_SESSION_ID", test.sessionID)

			args := append([]string{"--repo", options.repo, "--json"}, test.args...)
			if status, err := p.create(args); err != nil || status != 0 {
				t.Fatalf("create: status=%d err=%v", status, err)
			}

			tasks, err := loadTasks()
			if err != nil {
				t.Fatal(err)
			}

			if len(tasks) != 1 {
				t.Fatalf("expected one persisted task, got %d", len(tasks))
			}

			created := tasks[0]
			if created.Agent != test.agent || created.OwnerID != test.ownerID || created.State != taskActive {
				t.Fatalf("unexpected identity or state: %+v", created)
			}

			leaf := test.agent + "-" + created.ID[:8]
			if string(created.Path) != filepath.Join(options.primary, ".worktrees", leaf) || string(created.Branch) != "worktree/"+leaf {
				t.Fatalf("identity not used in allocation: %+v", created)
			}
		})
	}
}
