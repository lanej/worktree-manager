package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveGrace(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]time.Duration{"": 0, "0": 0, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour} {
		got, err := archiveGrace(archiveSettings{GracePeriod: value})
		if err != nil || got != want {
			t.Fatalf("%q: got %v, %v; want %v", value, got, err, want)
		}
	}

	for _, value := range []string{"-1h", "-1d", "later", "999999999999999999999d"} {
		if _, err := archiveGrace(archiveSettings{GracePeriod: value}); err == nil {
			t.Fatalf("accepted invalid grace %q", value)
		}
	}
}

func TestArchiveClock(t *testing.T) {
	t.Parallel()

	now := time.Unix(200000, 0)
	candidate := task{Agent: "codex", OwnerID: "owner", Path: "/archive-clock"}

	sessions := []archivedSession{{ID: "owner", Archived: true, ArchivedAt: now.Add(-2 * time.Hour).Unix(), UpdatedAt: now.Add(-time.Minute).Unix()}}
	if archiveEligible(candidate, sessions, now, time.Hour) {
		t.Fatal("last activity must restart the grace period")
	}

	if !archiveEligible(candidate, sessions, now, 0) {
		t.Fatal("zero grace should allow an archived owner")
	}

	sessions[0].ArchivedAt = 0
	if archiveEligible(candidate, sessions, now, 0) {
		t.Fatal("unknown archive time must retain protection")
	}
}

// A failed repository read must stop cleanup before any task is considered eligible.
func TestArchiveRepositoryFailure(t *testing.T) {
	root := t.TempDir()

	config := filepath.Join(root, "config.json")
	if err := os.WriteFile(config, []byte(`{"archive_cleanup":{"enabled":true,"grace_period":"0"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WORKTREE_CONFIG", config)

	unavailable := errors.New("session repository unavailable")

	p := &policy{ctx: t.Context(), archiveSessions: unavailableArchiveSessionRepository{err: unavailable}}
	if err := p.archiveSweep(true, true); !errors.Is(err, unavailable) {
		t.Fatalf("repository failure was not preserved: %v", err)
	}
}

type unavailableArchiveSessionRepository struct{ err error }

func (repository unavailableArchiveSessionRepository) List(context.Context) ([]archivedSession, error) {
	return nil, repository.err
}
