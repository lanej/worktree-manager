package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// codexSQLiteSessionRepository adapts Codex's local thread database without writing it.
// It resolves the newest database on every read so a running watcher sees upgrades.
type codexSQLiteSessionRepository struct{}

// Read only the current schema; missing archival timestamps or unavailable metadata retain tasks.
func (codexSQLiteSessionRepository) List(ctx context.Context) ([]archivedSession, error) {
	root := os.Getenv("CODEX_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}

		root = filepath.Join(home, ".codex")
	}

	files, err := filepath.Glob(filepath.Join(root, "state_*.sqlite"))
	if err != nil {
		return nil, err
	}

	newest := -1
	database := ""

	for _, file := range files {
		number, parseErr := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(file), "state_"), ".sqlite"))
		if parseErr == nil && number > newest {
			newest, database = number, file
		}
	}

	if database == "" {
		return nil, errors.New("codex state database unavailable; retaining worktrees")
	}
	//nolint:gosec // rationale: A local numeric-version database path and constant read-only SQL are separate argv entries.
	child := exec.CommandContext(ctx, "sqlite3", "-readonly", "-noheader", "-separator", "\t", database,
		"SELECT hex(id), hex(cwd), archived, coalesce(archived_at,0), updated_at FROM threads;")

	output, err := child.Output()
	if err != nil {
		return nil, fmt.Errorf("codex archive metadata unavailable: %w", err)
	}

	sessions := []archivedSession{}

	for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		if line == "" {
			continue
		}

		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			return nil, errors.New("invalid Codex archive metadata")
		}

		id, idErr := hex.DecodeString(fields[0])
		path, pathErr := hex.DecodeString(fields[1])
		archivedAt, archiveErr := strconv.ParseInt(fields[3], 10, 64)

		updatedAt, updateErr := strconv.ParseInt(fields[4], 10, 64)
		if idErr != nil || pathErr != nil || archiveErr != nil || updateErr != nil || (fields[2] != "0" && fields[2] != "1") {
			return nil, errors.New("invalid Codex archive metadata")
		}

		sessions = append(sessions, archivedSession{ID: string(id), Path: string(path), Archived: fields[2] == "1", ArchivedAt: archivedAt, UpdatedAt: updatedAt})
	}

	return sessions, nil
}
