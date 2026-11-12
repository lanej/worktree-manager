package main

import (
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode"
	"unicode/utf8"
)

func printable(value any) string {
	if value == nil {
		return "—"
	}

	text := fmt.Sprint(value)
	if !utf8.ValidString(text) || strings.IndexFunc(text, unicode.IsControl) >= 0 {
		return strconv.QuoteToASCII(text)
	}

	return text
}

func humanBytes(value any) string {
	if value == nil {
		return "unknown"
	}

	number, valid := new(big.Int).SetString(fmt.Sprint(value), 10)
	if !valid {
		return "unknown"
	}

	unit := new(big.Int).SetInt64(1)
	label := "B"

	for _, candidate := range []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"} {
		next := new(big.Int).Mul(unit, big.NewInt(1024))
		if number.Cmp(next) < 0 {
			break
		}

		unit = next
		label = candidate
	}

	if label == "B" {
		return number.String() + " B"
	}

	quotient := new(big.Rat).SetFrac(number, unit)

	return quotient.FloatString(1) + " " + label
}

func show(command string, value any) error {
	object, valid := value.(map[string]any)
	if !valid {
		return fmt.Errorf("invalid %s display result", command)
	}

	switch command {
	case "create", "finish":
		if _, err := fmt.Fprintf(os.Stdout, "Task %s: %s\nPath: %s\nBranch: %s\n", printable(object["id"]), printable(object["state"]), printable(object["path"]), printable(object["branch"])); err != nil {
			return err
		}

		return nil
	case "limits":
		for _, key := range []string{"max_worktrees", "max_bytes"} {
			shown := "disabled"
			if object[key] != nil {
				shown = printable(object[key])
				if key == "max_bytes" {
					shown = humanBytes(object[key]) + " (" + printable(object[key]) + " bytes)"
				}
			}

			if _, err := fmt.Fprintf(os.Stdout, "%s: %s\n", key, shown); err != nil {
				return err
			}
		}

		return nil
	case "status", "backfill":
		rows, validRows := object["repositories"].([]any)
		if !validRows {
			return fmt.Errorf("invalid %s repository list", command)
		}

		table := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(table, "REPOSITORY\tLINKED\tALLOCATED\tSTATUS"); err != nil {
			return err
		}

		for _, item := range rows {
			row, validRow := item.(map[string]any)
			if !validRow {
				return fmt.Errorf("invalid %s repository row", command)
			}

			count := "unknown"
			if state, validState := row["inventory"].(map[string]any); validState {
				count = printable(state["linked_count"])
			}

			status := "available"
			if row["available"] != true {
				status = "unavailable: " + printable(row["error"])
			}

			disk := "not measured"
			if command == "status" {
				disk = humanBytes(row["disk_bytes"])
			}

			if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", printable(row["common_gitdir"]), count, disk, status); err != nil {
				return err
			}
		}

		if err := table.Flush(); err != nil {
			return err
		}

		if totals, validTotals := object["totals"].(map[string]any); validTotals {
			if _, err := fmt.Fprintf(os.Stdout, "\nLinked worktrees: %s; allocated: %s; complete: %t\n", printable(totals["linked_count"]), humanBytes(totals["disk_bytes"]), object["complete"] == true); err != nil {
				return err
			}
		}

		if tasks, validTasks := object["tasks"].([]any); validTasks && len(tasks) > 0 {
			if _, err := fmt.Fprintln(os.Stdout, "\nTASK                              STATE       PATH"); err != nil {
				return err
			}

			for _, item := range tasks {
				task, validTask := item.(map[string]any)
				if !validTask {
					return fmt.Errorf("invalid task display row")
				}

				path := task["path"]
				if task["observed_path"] != nil {
					path = task["observed_path"]
				}

				if _, err := fmt.Fprintf(os.Stdout, "%s  %-10s  %s\n", printable(task["id"]), printable(task["state"]), printable(path)); err != nil {
					return err
				}
			}
		}

		if problems, validProblems := object["errors"].([]any); validProblems {
			for _, problem := range problems {
				if _, err := fmt.Fprintln(os.Stdout, "Error:", printable(problem)); err != nil {
					return err
				}
			}
		}

		return nil
	default:
		return fmt.Errorf("no display formatter for %s", command)
	}
}
