package tools

import (
	"fmt"
	"strings"
)

func UnifiedDiff(change FileChange) string {
	before := ""
	after := ""
	if change.Before != nil && change.Before.TextAvailable {
		before = change.Before.Text
	} else if change.Before != nil {
		return ""
	}
	if change.After != nil && change.After.TextAvailable {
		after = change.After.Text
	} else if change.After != nil {
		return ""
	}

	beforeLines := splitDiffLines(before)
	afterLines := splitDiffLines(after)
	ops := diffLineOps(beforeLines, afterLines)
	if len(ops) == 0 {
		return ""
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "--- a/%s\n", change.Path)
	fmt.Fprintf(&builder, "+++ b/%s\n", change.Path)
	fmt.Fprintf(&builder, "@@ -1,%d +1,%d @@\n", len(beforeLines), len(afterLines))
	for _, op := range ops {
		switch op.kind {
		case "equal":
			builder.WriteString(" ")
		case "delete":
			builder.WriteString("-")
		case "insert":
			builder.WriteString("+")
		}
		builder.WriteString(op.line)
		if !strings.HasSuffix(op.line, "\n") {
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

type diffLineOp struct {
	kind string
	line string
}

func splitDiffLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	return lines
}

func diffLineOps(before []string, after []string) []diffLineOp {
	table := make([][]int, len(before)+1)
	for i := range table {
		table[i] = make([]int, len(after)+1)
	}
	for i := len(before) - 1; i >= 0; i-- {
		for j := len(after) - 1; j >= 0; j-- {
			if before[i] == after[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else if table[i+1][j] >= table[i][j+1] {
				table[i][j] = table[i+1][j]
			} else {
				table[i][j] = table[i][j+1]
			}
		}
	}

	ops := make([]diffLineOp, 0)
	i, j := 0, 0
	for i < len(before) && j < len(after) {
		if before[i] == after[j] {
			ops = append(ops, diffLineOp{kind: "equal", line: before[i]})
			i++
			j++
		} else if table[i+1][j] >= table[i][j+1] {
			ops = append(ops, diffLineOp{kind: "delete", line: before[i]})
			i++
		} else {
			ops = append(ops, diffLineOp{kind: "insert", line: after[j]})
			j++
		}
	}
	for i < len(before) {
		ops = append(ops, diffLineOp{kind: "delete", line: before[i]})
		i++
	}
	for j < len(after) {
		ops = append(ops, diffLineOp{kind: "insert", line: after[j]})
		j++
	}
	return ops
}
