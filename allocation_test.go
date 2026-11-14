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
