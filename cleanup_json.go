package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func nulFields(path string) ([]string, error) {
	//nolint:gosec // rationale: Internal cleanup reads its own private scratch files using byte-preserving framing.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	if len(data) == 0 {
		return []string{}, nil
	}

	if data[len(data)-1] != 0 {
		return nil, fmt.Errorf("incomplete scratch record: %s", path)
	}

	chunks := bytes.Split(data[:len(data)-1], []byte{0})

	result := make([]string, len(chunks))
	for i, chunk := range chunks {
		result[i] = string(chunk)
	}

	return result, nil
}

func nullableNumber(text string) (any, error) {
	if text == "" {
		return nil, nil
	}

	number, err := strconv.ParseInt(text, 10, 64)

	return number, err
}

func conflictStats(added, deleted string) (map[string]any, error) {
	result := map[string]any{"added": nil, "deleted": nil, "binary": added == "-", "available": added != "?"}
	for key, value := range map[string]string{"added": added, "deleted": deleted} {
		if value == "-" || value == "?" || value == "" {
			continue
		}

		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, err
		}

		result[key] = number
	}

	return result, nil
}

func cleanupJSON(args []string) error {
	if len(args) != 9 {
		return errors.New("invalid internal cleanup record arguments")
	}

	scratch, root := args[0], args[1]

	fields, err := nulFields(filepath.Join(scratch, "json-rows"))
	if err != nil {
		return err
	}

	keys := []string{"action", "reason", "path", "repository", "base", "size_kib", "size_age", "head_age_seconds", "codex_metadata", "status", "accepted_conditions"}
	if len(fields)%len(keys) != 0 {
		return errors.New("incomplete cleanup row")
	}

	rows := []map[string]any{}

	for offset := 0; offset < len(fields); offset += len(keys) {
		row := map[string]any{}
		for i, key := range keys {
			row[key] = gitText(fields[offset+i])
		}

		repository := fields[offset+3]
		if !filepath.IsAbs(repository) {
			repository = filepath.Join(root, repository)
		}

		row["repository"] = gitText(repository)

		for _, field := range []struct {
			key   string
			index int
		}{{"size_kib", 5}, {"head_age_seconds", 7}} {
			value, numberErr := nullableNumber(fields[offset+field.index])
			if numberErr != nil {
				return numberErr
			}

			row[field.key] = value
		}

		accepted := []string{}
		if fields[offset+10] != "" {
			accepted = strings.Split(fields[offset+10], ",")
		}

		row["accepted_conditions"] = accepted
		sessions := []map[string]any{}

		sessionData, readErr := nulFields(filepath.Join(scratch, fmt.Sprintf("codex-%d.data", offset/len(keys))))
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}

		if len(sessionData)%4 != 0 {
			return errors.New("incomplete Codex session record")
		}

		for i := 0; i < len(sessionData); i += 4 {
			updated, parseErr := strconv.ParseInt(sessionData[i+2], 10, 64)
			if parseErr != nil {
				return parseErr
			}

			sessions = append(sessions, map[string]any{"id": gitText(sessionData[i]), "title": gitText(sessionData[i+1]), "updated_at": updated, "archived": sessionData[i+3] == "1"})
		}

		row["codex_sessions"] = sessions
		row["conflict_details"] = nil

		details, readErr := nulFields(filepath.Join(scratch, fmt.Sprintf("conflicts-%d.data", offset/len(keys))))
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}

		if readErr == nil {
			if len(details) < 5 || (len(details)-5)%5 != 0 {
				return errors.New("incomplete conflict record")
			}

			total, parseErr := strconv.ParseInt(details[0], 10, 64)
			if parseErr != nil {
				return parseErr
			}

			files := []map[string]any{}

			for i := 5; i < len(details); i += 5 {
				head, statsErr := conflictStats(details[i+1], details[i+2])
				if statsErr != nil {
					return statsErr
				}

				base, statsErr := conflictStats(details[i+3], details[i+4])
				if statsErr != nil {
					return statsErr
				}

				files = append(files, map[string]any{"path": gitText(details[i]), "head": head, "base": base})
			}

			var ancestor, note any
			if details[3] != "" {
				ancestor = details[3]
			}

			if details[4] != "" {
				note = details[4]
			}

			row["conflict_details"] = map[string]any{"total_files": total, "omitted_files": total - int64(len(files)), "head": details[1], "base": details[2], "merge_base": ancestor, "stats_note": note, "files": files}
		}

		rows = append(rows, row)
	}

	sort.SliceStable(rows, func(i, j int) bool {
		left, lok := rows[i]["size_kib"].(int64)

		right, rok := rows[j]["size_kib"].(int64)
		if lok != rok {
			return lok
		}

		return left < right
	})

	repositories, err := nulFields(filepath.Join(scratch, "json-repositories"))
	if err != nil {
		return err
	}

	age, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		return err
	}

	errorCount, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil {
		return err
	}

	minimum, err := nullableNumber(args[8])
	if err != nil {
		return err
	}

	accepted := []string{}

	for i, name := range []string{"dirty", "ignored", "unmerged", "recent"} {
		if args[4+i] == "1" {
			accepted = append(accepted, name)
		}
	}

	repositoryPaths := make([]gitText, len(repositories))
	for i, path := range repositories {
		repositoryPaths[i] = gitText(path)
	}

	return writeJSON(map[string]any{"root": gitText(root), "age_hours": age, "errors": errorCount, "min_size_kib": minimum, "accepted_checks": accepted, "repositories": repositoryPaths, "worktrees": rows})
}
