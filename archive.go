package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Archive cleanup is opt-in because archival changes the lifetime of task protection.
type archiveSettings struct {
	Enabled     bool   `json:"enabled"`
	GracePeriod string `json:"grace_period"`
}

type archivedSession struct {
	ID         string
	Path       string
	Archived   bool
	ArchivedAt int64
	UpdatedAt  int64
}

func archiveGrace(config archiveSettings) (time.Duration, error) {
	value := config.GracePeriod
	if value == "" {
		value = "0"
	}

	if strings.HasSuffix(value, "d") {
		days, err := strconv.ParseUint(strings.TrimSuffix(value, "d"), 10, 16)
		if err != nil {
			return 0, fmt.Errorf("invalid archive grace period: %w", err)
		}

		return time.Duration(days) * 24 * time.Hour, nil
	}

	duration, err := time.ParseDuration(value)
	if err != nil || duration < 0 {
		return 0, fmt.Errorf("invalid archive grace period %q: use 0 or a nonnegative duration such as 24h or 7d", value)
	}

	return duration, nil
}

// Read only the current schema; missing archival timestamps or unavailable metadata retain tasks.
func (p *policy) codexArchiveSessions() ([]archivedSession, error) {
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
	child := exec.CommandContext(p.ctx, "sqlite3", "-readonly", "-noheader", "-separator", "\t", database,
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

func archiveEligible(t task, sessions []archivedSession, now time.Time, grace time.Duration) bool {
	if t.Agent != "codex" || t.OwnerID == "" {
		return false
	}

	ownerArchived := false

	for _, session := range sessions {
		if session.ID == t.OwnerID {
			since := max(session.ArchivedAt, session.UpdatedAt)
			ownerArchived = session.Archived && session.ArchivedAt > 0 && !now.Before(time.Unix(since, 0).Add(grace))
		}

		if session.Archived {
			continue
		}

		path, err := canonical(session.Path)
		if err != nil {
			return false
		}

		if within(path, string(t.Path)) {
			return false
		}
	}

	return ownerArchived
}

func (p *policy) archiveProtected(t task) (bool, error) {
	reports, err := p.taskReports([]task{t})
	if err != nil {
		return true, err
	}

	if len(reports) != 1 || reports[0]["observation"] != "available" {
		return true, nil
	}

	observed, ok := reports[0]["observed_path"].(gitText)
	if !ok {
		return true, errors.New("archived task path unavailable")
	}

	t.Path = observed

	config, err := readSettings()
	if err != nil {
		return true, err
	}

	if !config.ArchiveCleanup.Enabled {
		return true, nil
	}

	grace, err := archiveGrace(config.ArchiveCleanup)
	if err != nil {
		return true, err
	}

	sessions, err := p.codexArchiveSessions()
	if err != nil {
		return true, err
	}

	return !archiveEligible(t, sessions, time.Now(), grace), nil
}

func (p *policy) archiveCommand(args []string) (int, error) {
	f := flags("cleanup-archived")
	apply := f.Bool("apply", false, "Retire eligible Codex tasks and safely prune their checkouts")
	machine := f.Bool("json", false, "Emit JSON")

	watch := f.Duration("watch", 0, "Repeat at this interval, for example 1m (requires --apply)")
	if err := f.Parse(args); err != nil {
		return 2, err
	}

	if f.NArg() != 0 || *watch < 0 || (*watch > 0 && !*apply) {
		return 2, errors.New("unexpected arguments; --watch requires --apply and a positive interval")
	}

	for {
		if err := p.archiveSweep(*apply, *machine); err != nil {
			return 1, err
		}

		if *watch == 0 {
			return 0, nil
		}

		timer := time.NewTimer(*watch)
		select {
		case <-p.ctx.Done():
			timer.Stop()
			return 0, nil
		case <-timer.C:
		}
	}
}

func (p *policy) archiveSweep(apply, machine bool) error {
	config, err := readSettings()
	if err != nil {
		return err
	}

	rows := []map[string]any{}

	var sweepErrors error

	if config.ArchiveCleanup.Enabled {
		grace, graceErr := archiveGrace(config.ArchiveCleanup)
		if graceErr != nil {
			return graceErr
		}

		sessions, sessionErr := p.codexArchiveSessions()
		if sessionErr != nil {
			return sessionErr
		}

		tasks, taskErr := loadTasks()
		if taskErr != nil {
			return taskErr
		}

		for _, t := range tasks {
			if t.Agent != "codex" || t.OwnerID == "" || (t.State != taskActive && t.State != taskFinished) {
				continue
			}

			reports, reportErr := p.taskReports([]task{t})
			if reportErr != nil {
				return reportErr
			}

			if len(reports) != 1 || reports[0]["observation"] != "available" {
				continue
			}

			observed, validPath := reports[0]["observed_path"].(gitText)
			if !validPath {
				return errors.New("archive task path unavailable")
			}

			t.Path = observed
			if !archiveEligible(t, sessions, time.Now(), grace) {
				continue
			}

			if _, statErr := os.Lstat(string(t.Path)); errors.Is(statErr, os.ErrNotExist) {
				continue
			}

			row := map[string]any{"id": t.ID, "path": t.Path, "outcome": "eligible"}
			rows = append(rows, row)

			if !apply {
				continue
			}

			if sweepErr := p.archiveTask(t, row); sweepErr != nil {
				row["outcome"] = "unknown"
				row["error"] = sweepErr.Error()
				sweepErrors = errors.Join(sweepErrors, sweepErr)
			}
		}
	}

	return errors.Join(p.result("cleanup-archived", map[string]any{"enabled": config.ArchiveCleanup.Enabled, "applied": apply, "complete": sweepErrors == nil, "tasks": rows}, machine), sweepErrors)
}

// Keep an archival fence after finishing, so ordinary prune also rechecks a reopened owner.
func (p *policy) archiveTask(t task, row map[string]any) (resultErr error) {
	unlock, err := operationLock(string(t.Common))
	if err != nil {
		return err
	}

	err = p.markArchiveTask(t)

	err = errors.Join(err, unlock())
	if err != nil {
		return err
	}

	executable, err := os.Executable()
	if err != nil {
		return err
	}
	//nolint:gosec // rationale: Run this same executable with a validated task identity as a separate argument.
	finish := exec.CommandContext(p.ctx, executable, "finish", t.ID, "--json")
	if output, finishErr := finish.CombinedOutput(); finishErr != nil {
		return fmt.Errorf("finish: %w: %s", finishErr, output)
	}
	//nolint:gosec // rationale: Run this same executable with an identity-checked checkout path as a separate argument.
	prune := exec.CommandContext(p.ctx, executable, "internal-archive-prune", t.ID, string(t.Path))

	output, pruneErr := prune.Output()
	if pruneErr != nil {
		if partial, decodeErr := decodeWire(output); decodeErr == nil {
			row["prune"] = partial
		}

		return fmt.Errorf("prune: %w", pruneErr)
	}

	decoded, err := decodeWire(output)
	if err != nil {
		return err
	}

	row["prune"] = decoded
	row["outcome"] = "unknown"

	envelope, ok := decoded.(map[string]any)
	if !ok {
		return errors.New("invalid prune envelope")
	}

	report, ok := envelope["result"].(map[string]any)
	if !ok {
		return errors.New("missing prune result")
	}

	worktrees, ok := report["worktrees"].([]any)
	if !ok {
		return errors.New("missing prune worktrees")
	}

	for _, item := range worktrees {
		checkout, valid := item.(map[string]any)
		if valid && checkout["path"] == string(t.Path) {
			row["outcome"] = checkout["outcome"]
		}
	}

	return nil
}

func (p *policy) markArchiveTask(selected task) error {
	tasks, err := loadTasks()
	if err != nil {
		return err
	}

	for _, t := range tasks {
		if t.ID != selected.ID {
			continue
		}

		if t.State != taskActive && t.State != taskFinished {
			return errors.New("task state changed")
		}

		t.Path = selected.Path

		dir, dirErr := p.gitDirAt(string(t.Path))
		if dirErr != nil {
			return dirErr
		}

		owner, ownerErr := registrationOwner(dir)

		common, commonErr := p.commonAt(string(t.Path))
		if ownerErr != nil || commonErr != nil || owner != t.ID || dir != string(t.GitDir) || common != string(t.Common) {
			return errors.New("task registration identity changed")
		}

		protected, archiveErr := p.archiveProtected(t)
		if archiveErr != nil {
			return archiveErr
		}

		if protected {
			return errors.New("archive eligibility changed")
		}

		t.ArchiveRetired = true

		return saveTask(t)
	}

	return errors.New("task disappeared")
}

// The cleanup engine invokes this fence again immediately before removal.
func (p *policy) archiveIdentity(id, path string) error {
	tasks, err := loadTasks()
	if err != nil {
		return err
	}

	for _, t := range tasks {
		if t.ID != id {
			continue
		}

		dir, dirErr := p.gitDirAt(path)
		if dirErr != nil {
			return dirErr
		}

		owner, ownerErr := registrationOwner(dir)

		common, commonErr := p.commonAt(path)
		if ownerErr != nil || commonErr != nil || owner != id || dir != string(t.GitDir) || common != string(t.Common) || !t.ArchiveRetired || t.State != taskFinished {
			return errors.New("archive cleanup registration changed")
		}

		protected, archiveErr := p.archiveProtected(t)
		if archiveErr != nil {
			return archiveErr
		}

		if protected {
			return errors.New("archive protection restored")
		}

		return nil
	}

	return errors.New("archive task missing")
}
