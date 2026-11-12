package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

//go:embed internal/cleanup.sh
var cleanupEngine []byte

type settings struct {
	RecoveryAllowedGitHubOwners []string `json:"recovery_allowed_github_owners"`
}

var (
	ownerPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	recoveryURLPattern = regexp.MustCompile(`^(?:https://github[.]com/|git@github[.]com:|ssh://git@github[.]com(?::22)?/)([a-z0-9][a-z0-9-]*)/[a-z0-9][a-z0-9._-]*$`)
)

func readSettings() (settings, error) {
	path := os.Getenv("WORKTREE_CONFIG")
	if path == "" {
		root := os.Getenv("XDG_CONFIG_HOME")
		if root == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return settings{}, err
			}

			root = filepath.Join(home, ".config")
		}

		path = filepath.Join(root, "worktree-manager", "config.json")
	}
	//nolint:gosec // rationale: This is an explicit caller-owned configuration file.
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && os.Getenv("WORKTREE_CONFIG") == "" {
		return settings{}, nil
	}

	if err != nil {
		return settings{}, err
	}

	var config settings

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err = decoder.Decode(&config); err != nil {
		return settings{}, err
	}

	for _, owner := range config.RecoveryAllowedGitHubOwners {
		if !ownerPattern.MatchString(owner) {
			return settings{}, fmt.Errorf("invalid approved GitHub owner: %q", owner)
		}
	}

	return config, nil
}

func recoveryAllowed(raw string) (bool, error) {
	config, err := readSettings()
	if err != nil {
		return false, err
	}

	match := recoveryURLPattern.FindStringSubmatch(strings.ToLower(raw))
	if match == nil {
		return false, nil
	}

	return contains(config.RecoveryAllowedGitHubOwners, match[1]), nil
}

type cleanupRequest struct {
	Args         []string
	Modern       bool
	Capture      bool
	Repositories []string
	Candidates   []string
}
type cleanupResult struct {
	Diagnostics []byte
	Output      []byte
	Status      int
	Removed     []string
}

func (p *policy) runCleanup(request cleanupRequest) (result cleanupResult, resultErr error) {
	stage, err := os.MkdirTemp("", "worktree-engine-")
	if err != nil {
		return result, err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(stage)) }()

	script := filepath.Join(stage, "cleanup.sh")
	if err = os.WriteFile(script, cleanupEngine, 0o600); err != nil {
		return result, err
	}

	executable, err := os.Executable()
	if err != nil {
		return result, err
	}

	env, err := p.repositoryEnvironment()
	if err != nil {
		return result, err
	}

	filtered := []string{}

	for _, value := range env {
		key, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(key, "WORKTREE_MANAGER_") && key != "TMPDIR" {
			filtered = append(filtered, value)
		}
	}

	filtered = append(filtered, "WORKTREE_MANAGER_EXECUTABLE="+executable, "TMPDIR="+stage)
	if request.Modern {
		filtered = append(filtered, "WORKTREE_MANAGER_MODERN=1")
	}

	for _, field := range []struct {
		name  string
		paths []string
	}{{"REPOSITORIES", request.Repositories}, {"CANDIDATES", request.Candidates}} {
		if field.paths == nil {
			continue
		}

		path := filepath.Join(stage, field.name)

		data := []byte{}
		for _, item := range field.paths {
			data = append(data, []byte(item)...)
			data = append(data, 0)
		}

		if err = os.WriteFile(path, data, 0o600); err != nil {
			return result, err
		}

		filtered = append(filtered, "WORKTREE_MANAGER_"+field.name+"="+path)
	}

	receipts := filepath.Join(stage, "receipts")
	if err = os.WriteFile(receipts, nil, 0o600); err != nil {
		return result, err
	}

	filtered = append(filtered, "WORKTREE_MANAGER_RECEIPTS="+receipts)
	//nolint:gosec // rationale: Execute only embedded shell source with caller input in separate argv entries.
	child := exec.CommandContext(p.ctx, "/bin/bash", append([]string{script}, request.Args...)...)
	child.Env = filtered
	child.Stderr = os.Stderr
	child.Stdin = os.Stdin
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	child.Cancel = func() error { return syscall.Kill(-child.Process.Pid, syscall.SIGKILL) }

	var output, diagnostics bytes.Buffer
	if request.Capture {
		child.Stderr = io.MultiWriter(os.Stderr, &diagnostics)
		child.Stdout = &output
	} else {
		child.Stdout = os.Stdout
	}

	runErr := child.Run()
	result.Output = output.Bytes()
	result.Diagnostics = diagnostics.Bytes()

	var exited *exec.ExitError
	if errors.As(runErr, &exited) {
		result.Status = max(1, exited.ExitCode())
	} else if runErr != nil {
		return result, runErr
	}

	result.Removed, err = nulFields(receipts)

	return result, err
}

func (p *policy) cleanup(args []string, modern, capture bool) (int, error) {
	result, err := p.runCleanup(cleanupRequest{Args: args, Modern: modern, Capture: capture})
	return result.Status, err
}

func (p *policy) prune(args []string) (status int, resultErr error) {
	cleanArgs, machine, all, apply, parseErr := pruneArguments(args)
	if parseErr != nil {
		return 2, parseErr
	}

	if contains(cleanArgs, "-h") || contains(cleanArgs, "--help") {
		result, err := p.runCleanup(cleanupRequest{Args: []string{"-h"}, Modern: true})
		return result.Status, err
	}

	if _, err := readSettings(); err != nil {
		return 1, err
	}

	var repositories []string
	if all {
		repositories = []string{}

		records, err := p.records()
		if err != nil {
			return 1, err
		}

		for _, record := range records {
			if common, ok := record["common_gitdir"].(gitText); ok {
				repositories = append(repositories, string(common))
			} else {
				return 1, errors.New("registry contains an unidentified repository; inspect policy status --all")
			}
		}
	}

	if !apply && !machine {
		result, err := p.runCleanup(cleanupRequest{Args: cleanArgs, Modern: true, Repositories: repositories})
		return result.Status, err
	}

	previewArgs := append([]string{"--json"}, cleanArgs...)

	preview, err := p.runCleanup(cleanupRequest{Args: previewArgs, Modern: true, Capture: true, Repositories: repositories})
	data, code := preview.Output, preview.Status

	if err != nil {
		return 1, err
	}

	decoded, decodeErr := decodeWire(data)
	if decodeErr != nil {
		return 1, fmt.Errorf("cleanup did not produce a complete report (exit %d): %w", code, decodeErr)
	}

	report, validReport := decoded.(map[string]any)
	if !validReport {
		return 1, errors.New("invalid cleanup report")
	}

	if !apply {
		report["complete"] = code == 0
		report["outcome"] = "preview"
		report["inspection_diagnostics"] = gitText(preview.Diagnostics)

		return code, p.result("prune", report, true)
	}

	paths, ok := report["repositories"].([]any)
	if !ok {
		return 1, errors.New("missing cleanup repository list")
	}

	commonToPrimary := map[string]string{}

	for _, item := range paths {
		primary, validPath := item.(string)
		if !validPath {
			return 1, errors.New("invalid cleanup repository path")
		}

		common, commonErr := p.commonAt(primary)
		if commonErr != nil {
			return 1, commonErr
		}

		commonToPrimary[common] = primary
	}

	commons := []string{}
	for common := range commonToPrimary {
		commons = append(commons, common)
	}

	sort.Strings(commons)

	releases := []func() error{}
	defer func() {
		for i := len(releases) - 1; i >= 0; i-- {
			resultErr = errors.Join(resultErr, releases[i]())
		}
	}()

	available := []string{}
	lockProblems := []string{}

	for _, common := range commons {
		release, lockErr := operationLock(common)
		if lockErr != nil {
			lockProblems = append(lockProblems, common+": "+lockErr.Error())
			continue
		}

		releases = append(releases, release)
		available = append(available, commonToPrimary[common])
	}

	rows, validRows := report["worktrees"].([]any)
	if !validRows {
		return 1, errors.New("missing cleanup rows")
	}

	candidates := []string{}

	for _, item := range rows {
		row, valid := item.(map[string]any)
		if !valid {
			return 1, errors.New("invalid cleanup row")
		}

		action, valid := row["action"].(string)
		if !valid {
			return 1, errors.New("missing cleanup action")
		}

		if removalAction(action) {
			path, pathOK := row["path"].(string)
			if !pathOK {
				return 1, errors.New("missing checkout path")
			}

			candidates = append(candidates, path)
		}
	}

	applyArgs := append([]string{"-y"}, cleanArgs...)

	applied, err := p.runCleanup(cleanupRequest{Args: applyArgs, Modern: true, Capture: machine, Repositories: available, Candidates: candidates})
	if err != nil {
		return 1, err
	}

	for _, common := range commons {
		if _, reconcileErr := p.reconcile(common); reconcileErr != nil {
			lockProblems = append(lockProblems, common+": "+reconcileErr.Error())
		}
	}

	status = max(code, applied.Status)
	if len(lockProblems) > 0 {
		status = 1
	}

	if !machine {
		for _, problem := range lockProblems {
			if _, printErr := fmt.Fprintln(os.Stderr, "Retaining repository:", problem); printErr != nil {
				return 1, printErr
			}
		}

		return status, nil
	}

	for _, item := range rows {
		row, ok := item.(map[string]any)
		if !ok {
			return 1, errors.New("invalid cleanup row")
		}

		action, validAction := row["action"].(string)
		if !validAction {
			return 1, errors.New("missing cleanup action")
		}

		row["outcome"] = "not_attempted"

		if !removalAction(action) {
			continue
		}

		path, ok := row["path"].(string)
		if !ok {
			return 1, errors.New("missing checkout path")
		}

		repository, ok := row["repository"].(string)
		if !ok {
			return 1, errors.New("missing repository path")
		}

		common, commonErr := p.commonAt(repository)
		if commonErr != nil {
			row["outcome"] = "unknown"
			status = 1

			continue
		}

		inventory, inventoryErr := p.inventory(common)
		if inventoryErr != nil {
			row["outcome"] = "unknown"
			status = 1

			continue
		}

		registered := false

		for _, checkout := range inventory.Worktrees {
			if candidate, ok := checkout["worktree"].(gitText); ok && string(candidate) == path {
				registered = true
				break
			}
		}

		_, statErr := os.Lstat(path)
		if !registered && errors.Is(statErr, os.ErrNotExist) && contains(applied.Removed, path) {
			row["outcome"] = "removed"
		} else if !registered || statErr != nil || contains(applied.Removed, path) {
			row["outcome"] = "unknown"
			status = 1
		} else {
			row["outcome"] = "retained"
		}
	}

	report["outcome"] = "applied"
	report["complete"] = status == 0
	report["mutation_log"] = gitText(applied.Output)
	report["mutation_diagnostics"] = gitText(applied.Diagnostics)
	report["mutation_errors"] = lockProblems

	return status, p.result("prune", report, true)
}

func removalAction(action string) bool {
	return action == "remove" || action == "force-remove" || action == "wip-push"
}

func pruneArguments(args []string) ([]string, bool, bool, bool, error) {
	options := []string{}
	operands := []string{}
	machine, all, apply := false, false, false
	selectedPath := false
	values := map[string]bool{"-a": true, "-b": true, "--worktree": true, "--min-size": true, "--conflict-files": true}
	switches := map[string]bool{"--refresh": true, "--wip-push": true, "--force": true, "--accept-dirty": true, "--accept-ignored": true, "--accept-unmerged": true, "--accept-recent": true, "--integrated": true, "--help": true, "-h": true}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			operands = append(operands, args[i+1:]...)
			break
		}

		switch arg {
		case "--json":
			machine = true
		case "--all":
			all = true
		case "--apply", "-y":
			apply = true
		default:
			if values[arg] {
				if i+1 >= len(args) {
					return nil, false, false, false, fmt.Errorf("%s needs a value", arg)
				}

				i++

				options = append(options, arg, args[i])
				if arg == "--worktree" {
					selectedPath = true
				}
			} else if switches[arg] {
				options = append(options, arg)
			} else if strings.HasPrefix(arg, "--") {
				key, value, hasValue := strings.Cut(arg, "=")
				if !hasValue || !values[key] {
					return nil, false, false, false, fmt.Errorf("unknown prune option: %s", arg)
				}

				options = append(options, key, value)
				if key == "--worktree" {
					selectedPath = true
				}
			} else if strings.HasPrefix(arg, "-") && len(arg) > 1 {
				for j := 1; j < len(arg); j++ {
					switch arg[j] {
					case 'y':
						apply = true
					case 'h':
						options = append(options, "-h")
					case 'a', 'b':
						key := "-" + string(arg[j])

						value := arg[j+1:]
						if value == "" {
							if i+1 >= len(args) {
								return nil, false, false, false, fmt.Errorf("%s needs a value", key)
							}

							i++
							value = args[i]
						}

						options = append(options, key, value)
						j = len(arg)
					default:
						return nil, false, false, false, fmt.Errorf("unknown prune option: %s", arg)
					}
				}
			} else {
				operands = append(operands, arg)
			}
		}
	}

	if len(operands) > 1 || (all && (len(operands) > 0 || selectedPath)) || (selectedPath && len(operands) > 0) {
		return nil, false, false, false, errors.New("choose one of ROOT, --all or --worktree PATH")
	}

	if len(operands) > 0 {
		options = append(options, "--")
		options = append(options, operands...)
	}

	return options, machine, all, apply, nil
}
