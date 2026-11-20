package main

import (
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
