package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func (p *policy) discover(roots []string) ([]string, []string) {
	seen := map[string]bool{}
	repositories := []string{}
	problems := []string{}
	register := func(path string) bool {
		common, err := p.commonAt(path)
		if err != nil {
			return false
		}

		if !seen[common] {
			seen[common] = true
			repositories = append(repositories, common)
		}

		return true
	}
	excluded := map[string]bool{".git": true, "node_modules": true, ".venv": true, "target": true, ".worktrees": true, ".treehouse": true, "__pycache__": true}

	for _, root := range roots {
		resolved, err := canonical(root)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", root, err))
			continue
		}

		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			problems = append(problems, fmt.Sprintf("%s: not an accessible directory", root))
			continue
		}

		if register(resolved) {
			continue
		}

		err = filepath.WalkDir(resolved, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				problems = append(problems, fmt.Sprintf("%s: %s", path, walkErr))

				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}

				return nil
			}

			if !entry.IsDir() {
				return nil
			}

			if path != resolved && excluded[entry.Name()] {
				return filepath.SkipDir
			}

			if _, statErr := os.Lstat(filepath.Join(path, ".git")); statErr == nil {
				if !register(path) {
					problems = append(problems, "invalid repository: "+path)
				}

				return filepath.SkipDir
			} else if !errors.Is(statErr, os.ErrNotExist) {
				problems = append(problems, fmt.Sprintf("%s: %s", path, statErr))
				return filepath.SkipDir
			}

			return nil
		})
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", resolved, err))
		}
	}

	return repositories, problems
}

func (p *policy) backfill(args []string) (int, error) {
	args, machine := jsonFlag(args)
	if len(args) == 0 {
		return 2, errors.New("backfill requires explicit ROOT arguments; it never scans your home implicitly")
	}

	repositories, problems := p.discover(args)
	records := []map[string]any{}

	for _, common := range repositories {
		state, err := p.reconcile(common)

		record := map[string]any{"common_gitdir": gitText(common), "available": err == nil}
		if err != nil {
			record["error"] = err.Error()
			problems = append(problems, common+": "+err.Error())
		} else {
			record["inventory"] = state
		}

		records = append(records, record)
	}

	code := 0
	if len(problems) > 0 {
		code = 1
	}

	return code, p.result("backfill", map[string]any{"complete": code == 0, "repositories": records, "errors": problems}, machine)
}
