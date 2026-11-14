package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var agentPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

var errAllocationRejected = errors.New("allocation rejected")

type allocationOptions struct {
	repo, common, primary, agent, branch, path string
}

type taskAllocation struct {
	id, branch, path string
}

func randomTaskID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}

	return hex.EncodeToString(random), nil
}

func (p *policy) branchExists(repo, branch string) (bool, error) {
	if _, err := p.workingGit(repo, "check-ref-format", "--branch", branch); err != nil {
		return false, errors.Join(errAllocationRejected, err)
	}

	output, err := p.workingGit(repo, "for-each-ref", "--format=%(refname)", "refs/heads/")
	if err != nil {
		return false, err
	}

	ref := "refs/heads/" + branch
	for existing := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		if existing == ref || strings.HasPrefix(existing, ref+"/") {
			return true, nil
		}

		if existing != "" && strings.HasPrefix(ref, existing+"/") {
			return false, fmt.Errorf("%w: branch namespace conflicts with existing branch %s", errAllocationRejected, strings.TrimPrefix(existing, "refs/heads/"))
		}
	}

	return false, nil
}

func (p *policy) allocationPath(options allocationOptions, leaf string) (string, error) {
	destination := options.path
	if destination == "" {
		destination = filepath.Join(options.primary, ".worktrees", leaf)
	}

	path, err := canonical(destination)
	if err != nil {
		return "", err
	}

	if within(path, options.common) {
		return "", errors.New("checkout cannot be placed inside Git's administrative directory")
	}

	if options.path == "" {
		primary, primaryErr := canonical(options.primary)
		if primaryErr != nil {
			return "", primaryErr
		}

		root := filepath.Join(primary, ".worktrees")
		if path != filepath.Join(root, leaf) {
			return "", errors.New("default destination must remain inside the primary checkout's .worktrees directory without symlink redirection")
		}

		if _, err = p.workingGit(primary, "check-ignore", "-q", "--no-index", "--", destination); err != nil {
			return "", errors.New("default .worktrees directory must be ignored; add .worktrees/ to .gitignore or choose --path outside the repository")
		}
	}

	return path, nil
}

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	return err == nil, err
}

func (p *policy) allocate(options allocationOptions, nextID func() (string, error)) (taskAllocation, error) {
	if options.branch != "" {
		occupied, err := p.branchExists(options.repo, options.branch)
		if err != nil {
			return taskAllocation{}, err
		}

		if occupied {
			return taskAllocation{}, fmt.Errorf("%w: branch already exists; create requires a fresh named branch", errAllocationRejected)
		}
	}

	root, err := tasksDir()
	if err != nil {
		return taskAllocation{}, err
	}

	for range 16 {
		id, idErr := nextID()
		if idErr != nil {
			return taskAllocation{}, idErr
		}

		if !taskIDPattern.MatchString(id) {
			return taskAllocation{}, errors.New("invalid generated task ID")
		}

		occupied, recordErr := exists(filepath.Join(root, id+".json"))
		if recordErr != nil {
			return taskAllocation{}, recordErr
		}

		if occupied {
			continue
		}

		leaf := options.agent + "-" + id[:8]

		path, pathErr := p.allocationPath(options, leaf)
		if pathErr != nil {
			return taskAllocation{}, pathErr
		}

		occupied, pathErr = exists(path)
		if pathErr != nil {
			return taskAllocation{}, pathErr
		}

		if occupied {
			if options.path != "" {
				return taskAllocation{}, errors.New("destination already exists; worktrees are never reused")
			}

			continue
		}

		branch := options.branch
		if branch == "" {
			branch = "worktree/" + leaf

			occupied, branchErr := p.branchExists(options.repo, branch)
			if branchErr != nil {
				return taskAllocation{}, branchErr
			}

			if occupied {
				continue
			}
		}

		return taskAllocation{id: id, branch: branch, path: path}, nil
	}

	return taskAllocation{}, fmt.Errorf("could not allocate an unused %s checkout identity after 16 attempts", options.agent)
}
