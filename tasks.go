package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

type taskState string

const (
	taskCreating  taskState = "creating"
	taskActive    taskState = "active"
	taskFinished  taskState = "finished"
	taskFailed    taskState = "failed"
	taskUncertain taskState = "uncertain"
)

type task struct {
	ID         string     `json:"id"`
	Common     gitText    `json:"common_gitdir"`
	GitDir     gitText    `json:"gitdir"`
	Path       gitText    `json:"path"`
	Branch     gitText    `json:"branch"`
	State      taskState  `json:"state"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
}

var taskIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (t task) protected() (bool, error) {
	switch t.State {
	case taskCreating, taskActive, taskUncertain:
		return true, nil
	case taskFinished, taskFailed:
		return false, nil
	default:
		return false, fmt.Errorf("invalid task state %q for %s", t.State, t.ID)
	}
}

func tasksDir() (string, error) {
	root, err := registryDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(filepath.Dir(filepath.Dir(root)), "worktree-manager", "tasks"), nil
}

func saveTask(t task) error {
	if !taskIDPattern.MatchString(t.ID) {
		return errors.New("invalid task ID")
	}

	root, err := tasksDir()
	if err != nil {
		return err
	}

	return atomicJSON(filepath.Join(root, t.ID+".json"), t)
}

func loadTasks() ([]task, error) {
	root, err := tasksDir()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []task{}, nil
	}

	if err != nil {
		return nil, err
	}

	result := []task{}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		//nolint:gosec // rationale: Read a record in the caller-owned state directory.
		data, readErr := os.ReadFile(filepath.Join(root, entry.Name()))
		if readErr != nil {
			return nil, readErr
		}

		var t task
		if decodeErr := json.Unmarshal(data, &t); decodeErr != nil {
			return nil, decodeErr
		}

		if !taskIDPattern.MatchString(t.ID) || entry.Name() != t.ID+".json" || t.Common == "" || t.Path == "" {
			return nil, fmt.Errorf("invalid task record: %s", entry.Name())
		}

		if _, stateErr := t.protected(); stateErr != nil {
			return nil, stateErr
		}

		result = append(result, t)
	}

	return result, nil
}

func (p *policy) workingGit(directory string, args ...string) ([]byte, error) {
	env, err := p.repositoryEnvironment()
	if err != nil {
		return nil, err
	}
	//nolint:gosec // rationale: A caller-selected repository and Git arguments are passed separately, without a shell.
	child := exec.CommandContext(p.ctx, "git", append([]string{"-C", directory}, args...)...)
	child.Env = env

	var stderr bytes.Buffer

	child.Stderr = &stderr

	output, err := child.Output()
	if err != nil {
		return output, fmt.Errorf("%s: %w", strings.TrimSpace(stderr.String()), err)
	}

	return output, nil
}

func (p *policy) commonAt(directory string) (string, error) {
	output, err := p.workingGit(directory, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}

	return canonical(strings.TrimSuffix(string(output), "\n"))
}

func (p *policy) gitDirAt(directory string) (string, error) {
	output, err := p.workingGit(directory, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}

	return canonical(strings.TrimSuffix(string(output), "\n"))
}

func operationLock(directory string) (func() error, error) {
	if info, err := os.Stat(directory); err != nil {
		return nil, err
	} else if !info.IsDir() {
		return nil, errors.New("repository directory is unavailable")
	}

	root := filepath.Join(directory, "worktree-manager")
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, err
	}
	//nolint:gosec // rationale: The advisory lock is inside the explicitly selected repository, not a remote input path.
	file, err := os.OpenFile(filepath.Join(root, "operation.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}

	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.Join(errors.New("another worktree mutation is running; retry after it finishes"), err, file.Close())
	}

	return func() error { return errors.Join(syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close()) }, nil
}

const registrationMarker = "worktree-manager-task.json"

func registrationOwner(directory string) (string, error) {
	//nolint:gosec // rationale: The token lives in the already resolved native Git registration, not in checkout files.
	data, err := os.ReadFile(filepath.Join(directory, registrationMarker))
	if err != nil {
		return "", err
	}

	var record struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(data, &record); err != nil {
		return "", err
	}

	if !taskIDPattern.MatchString(record.ID) {
		return "", errors.New("invalid task registration token")
	}

	return record.ID, nil
}

func pinRegistration(directory, id string) error {
	owner, err := registrationOwner(directory)
	if err == nil {
		if owner != id {
			return errors.New("git registration already belongs to another task")
		}

		return nil
	}

	if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return atomicJSON(filepath.Join(directory, registrationMarker), map[string]string{"id": id})
}

func (p *policy) activeAt(path string) (bool, error) {
	tasks, err := loadTasks()
	if err != nil {
		return false, err
	}

	path, err = canonical(path)
	if err != nil {
		return false, err
	}

	var gitdir, common, owner, lockToken string

	inspected := false

	for _, t := range tasks {
		protected, stateErr := t.protected()
		if stateErr != nil {
			return false, stateErr
		}

		if !protected {
			continue
		}

		if !inspected {
			gitdir, err = p.gitDirAt(path)
			if err != nil {
				if string(t.Path) == path {
					return true, nil
				}

				return false, err
			}

			common, err = p.commonAt(path)
			if err != nil {
				return false, err
			}

			owner, err = registrationOwner(gitdir)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return false, err
			}
			//nolint:gosec // rationale: A native Git lock is secondary identity evidence after an interrupted creation.
			reason, readErr := os.ReadFile(filepath.Join(gitdir, "locked"))
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				return false, readErr
			}

			lockToken = strings.TrimSpace(string(reason))
			inspected = true
		}

		if owner == t.ID || lockToken == "worktree-manager:"+t.ID {
			return true, nil
		}

		if t.GitDir == "" && common == string(t.Common) {
			return true, nil
		}

		if owner == "" && t.GitDir != "" && gitdir == string(t.GitDir) {
			return true, nil
		}

		reports, reportErr := p.taskReports([]task{t})
		if reportErr != nil {
			return false, reportErr
		}

		for _, report := range reports {
			candidate := t.Path
			if observed, valid := report["observed_path"].(gitText); valid {
				candidate = observed
			}

			nested, pathErr := canonical(string(candidate))
			if pathErr != nil {
				return false, pathErr
			}
			// rationale: An ancestor removal deletes descendants too; unresolved tasks retain their recorded location conservatively.
			if nested != path && within(nested, path) {
				return true, nil
			}
		}
	}

	return false, nil
}

func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

func (p *policy) create(args []string) (status int, resultErr error) {
	args, machine := jsonFlag(args)
	f := flags("create")
	branch := f.String("branch", "", "new branch name")
	base := f.String("base", "HEAD", "local base revision")
	destination := f.String("path", "", "new checkout directory")

	repo := f.String("repo", ".", "existing repository")
	if err := f.Parse(args); err != nil {
		return 2, err
	}

	if f.NArg() != 0 || *branch == "" {
		return 2, errors.New("create requires --branch NAME and accepts no positional arguments")
	}

	common, err := p.commonAt(*repo)
	if err != nil {
		return 1, err
	}

	unlock, err := operationLock(common)
	if err != nil {
		return 1, err
	}

	defer func() { resultErr = errors.Join(resultErr, unlock()) }()

	if _, err = p.workingGit(*repo, "check-ref-format", "--branch", *branch); err != nil {
		return 1, err
	}

	_, branchErr := p.workingGit(*repo, "show-ref", "--verify", "--quiet", "refs/heads/"+*branch)
	if branchErr == nil {
		return 2, errors.New("branch already exists; create requires a fresh named branch")
	}

	var branchExit *exec.ExitError
	if !errors.As(branchErr, &branchExit) || branchExit.ExitCode() != 1 {
		return 1, branchErr
	}

	if _, err = p.workingGit(*repo, "rev-parse", "--verify", "--end-of-options", *base+"^{commit}"); err != nil {
		return 1, err
	}

	state, err := p.reconcile(common)
	if err != nil {
		return 1, err
	}

	capacity, err := p.capacityAt(common, state, true)
	if err != nil {
		return 1, err
	}

	if capacity != 0 {
		return 1, errBudgetFull
	}

	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return 1, err
	}

	id := hex.EncodeToString(random)

	if *destination == "" {
		if len(state.Worktrees) == 0 {
			return 1, errors.New("repository has no primary checkout")
		}

		primary, ok := state.Worktrees[0]["worktree"].(gitText)
		if !ok || state.Worktrees[0]["bare"] == true {
			return 1, errors.New("bare repositories require an explicit --path")
		}

		*destination = filepath.Join(string(primary), ".worktrees", "task-"+id)
		if _, err = p.workingGit(string(primary), "check-ignore", "-q", "--no-index", "--", *destination); err != nil {
			return 1, errors.New("default .worktrees directory must be ignored; add .worktrees/ to .gitignore or choose --path outside the repository")
		}
	}

	path, err := canonical(*destination)
	if err != nil {
		return 1, err
	}

	if within(path, common) {
		return 1, errors.New("checkout cannot be placed inside Git's administrative directory")
	}

	if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return 1, errors.New("destination already exists; worktrees are never reused")
		}

		return 1, err
	}

	t := task{ID: id, Common: gitText(common), Path: gitText(path), Branch: gitText(*branch), State: taskCreating, CreatedAt: time.Now().UTC()}
	if err = saveTask(t); err != nil {
		return 1, err
	}

	_, gitErr := p.workingGit(*repo, "worktree", "add", "--lock", "--reason", "worktree-manager:"+id, "-b", *branch, "--", path, *base)
	actual, reconcileErr := p.reconcile(common)
	found := false

	if reconcileErr == nil {
		for _, row := range actual.Worktrees {
			candidate, valid := row["worktree"].(gitText)
			if !valid {
				continue
			}

			dir, dirErr := p.gitDirAt(string(candidate))
			if dirErr != nil {
				continue
			}
			//nolint:gosec // rationale: The native lock token identifies this task even if a checkout hook moved it.
			reason, readErr := os.ReadFile(filepath.Join(dir, "locked"))
			if readErr == nil && strings.TrimSpace(string(reason)) == "worktree-manager:"+id {
				found = true
				t.Path = candidate
				t.GitDir = gitText(dir)

				break
			}

			if string(candidate) == path {
				t.GitDir = gitText(dir)
			}
		}
	}

	if found {
		t.State = taskActive
		if markerErr := pinRegistration(string(t.GitDir), t.ID); markerErr != nil {
			t.State = taskUncertain
			reconcileErr = errors.Join(reconcileErr, markerErr)
		}
	} else {
		t.State = taskUncertain
		reconcileErr = errors.Join(reconcileErr, errors.New("could not confirm a surviving task checkout with its protection token; inspect repository state before retrying"))
	}

	combined := errors.Join(gitErr, reconcileErr)
	if combined != nil {
		t.Error = combined.Error()
		status = 1
	}

	if err = saveTask(t); err != nil {
		return 1, errors.Join(combined, err)
	}

	if err = p.result("create", t, machine); err != nil {
		return 1, errors.Join(combined, err)
	}

	return status, combined
}

func (p *policy) finish(args []string) (status int, resultErr error) {
	args, machine := jsonFlag(args)
	if len(args) != 1 {
		return 2, errors.New("finish requires a task ID or created checkout path")
	}

	tasks, err := loadTasks()
	if err != nil {
		return 1, err
	}

	path, pathErr := canonical(args[0])

	requestedID := ""
	if taskIDPattern.MatchString(args[0]) {
		requestedID = args[0]
	} else if pathErr == nil {
		if dir, dirErr := p.gitDirAt(path); dirErr == nil {
			owner, ownerErr := registrationOwner(dir)
			if ownerErr == nil {
				requestedID = owner
			} else if !errors.Is(ownerErr, os.ErrNotExist) {
				return 1, ownerErr
			}

			if requestedID == "" {
				return 1, errors.New("checkout has no managed task identity; use a known task ID for historical or interrupted tasks")
			}
		}
	}

	var selected *task

	for _, t := range tasks {
		if (requestedID != "" && t.ID == requestedID) || (requestedID == "" && pathErr == nil && string(t.Path) == path) {
			copyTask := t

			if selected != nil {
				return 1, errors.New("multiple historical tasks match this path; specify the task ID")
			}

			selected = &copyTask
		}
	}

	if selected == nil {
		return 1, errors.New("no managed task matches; finish does not adopt external checkouts")
	}

	t := *selected

	unlock, err := operationLock(string(t.Common))
	if err != nil {
		return 1, err
	}

	defer func() { resultErr = errors.Join(resultErr, unlock()) }()
	// rationale: Reload after acquiring the mutation lock so finish cannot overwrite a newer task state.
	tasks, err = loadTasks()
	if err != nil {
		return 1, err
	}

	for _, latest := range tasks {
		if latest.ID == t.ID {
			t = latest
			break
		}
	}

	if t.State == taskFinished {
		return 0, p.result("finish", t, machine)
	}

	state, err := p.inventory(string(t.Common))
	if err != nil {
		return 1, err
	}

	for _, row := range state.Worktrees {
		candidate, ok := row["worktree"].(gitText)
		if !ok {
			continue
		}

		dir, dirErr := p.gitDirAt(string(candidate))
		if dirErr != nil {
			continue
		}

		if t.GitDir != "" && dir != string(t.GitDir) {
			continue
		}
		//nolint:gosec // rationale: Inspect only the selected worktree's native lock, preserving locks owned by other callers.
		reason, readErr := os.ReadFile(filepath.Join(dir, "locked"))
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return 1, readErr
		}

		if strings.TrimSpace(string(reason)) == "worktree-manager:"+t.ID {
			if _, unlockErr := p.git(string(t.Common), false, "worktree", "unlock", string(candidate)); unlockErr != nil {
				return 1, unlockErr
			}

			t.Path = candidate
			t.GitDir = gitText(dir)
		}
	}

	now := time.Now().UTC()
	t.State = taskFinished

	t.FinishedAt = &now
	if err = saveTask(t); err != nil {
		return 1, err
	}

	return 0, p.result("finish", t, machine)
}

func (p *policy) taskReports(tasks []task) ([]map[string]any, error) {
	rows := []map[string]any{}

	for _, t := range tasks {
		data, err := json.Marshal(t)
		if err != nil {
			return nil, err
		}

		decoded, err := decodeWire(data)
		if err != nil {
			return nil, err
		}

		row, valid := decoded.(map[string]any)
		if !valid {
			return nil, errors.New("invalid task report")
		}

		row["observed_path"] = nil
		row["observation"] = "unknown"

		state, inventoryErr := p.inventory(string(t.Common))
		if inventoryErr != nil {
			row["observation_error"] = inventoryErr.Error()
			rows = append(rows, row)

			continue
		}

		row["observation"] = "missing"

		for _, checkout := range state.Worktrees {
			path, validPath := checkout["worktree"].(gitText)
			if !validPath {
				continue
			}

			dir, dirErr := p.gitDirAt(string(path))
			if dirErr != nil {
				continue
			}

			owner, ownerErr := registrationOwner(dir)

			matches := ownerErr == nil && owner == t.ID
			if !matches && string(t.GitDir) == dir && t.State != taskFinished && t.State != taskFailed {
				row["observation"] = "identity_unverified"
			}

			if !matches {
				//nolint:gosec // rationale: Match the task's own native lock token while observing existing Git registrations.
				reason, readErr := os.ReadFile(filepath.Join(dir, "locked"))
				matches = readErr == nil && strings.TrimSpace(string(reason)) == "worktree-manager:"+t.ID
			}

			if matches {
				row["observed_path"] = path
				row["observation"] = "available"

				break
			}
		}

		rows = append(rows, row)
	}

	return rows, nil
}
