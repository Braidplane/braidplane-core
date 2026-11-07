package frontend

import (
	"fmt"
	"strings"
)

const (
	unifiedDiffMaxLines = 1200
	unifiedDiffMaxCells = 200000
)

type unifiedDiffOpKind int

const (
	unifiedDiffEqual unifiedDiffOpKind = iota
	unifiedDiffDelete
	unifiedDiffInsert
)

type unifiedDiffOp struct {
	kind unifiedDiffOpKind
	line string
}

func unifiedLineDiff(
	fromFile string,
	toFile string,
	before []byte,
	after []byte,
	contextLines int,
) string {
	beforeLines := splitLinesPreserveNewline(string(before))
	afterLines := splitLinesPreserveNewline(string(after))
	if unifiedDiffTooLarge(beforeLines, afterLines) {
		return unifiedDiffTooLargeMessage(fromFile, toFile, len(beforeLines), len(afterLines))
	}
	ops := unifiedDiffOperations(beforeLines, afterLines)

	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n", fromFile)
	fmt.Fprintf(&b, "+++ %s\n", toFile)

	hunks := unifiedDiffHunks(ops, contextLines)
	for _, hunk := range hunks {
		oldStart, oldCount, newStart, newCount := unifiedDiffRange(ops, hunk.start, hunk.end)
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, op := range ops[hunk.start:hunk.end] {
			switch op.kind {
			case unifiedDiffEqual:
				b.WriteString(" ")
			case unifiedDiffDelete:
				b.WriteString("-")
			case unifiedDiffInsert:
				b.WriteString("+")
			}
			b.WriteString(op.line)
			if !strings.HasSuffix(op.line, "\n") {
				b.WriteString("\n")
			}
		}
	}

	return b.String()
}

func splitLinesPreserveNewline(input string) []string {
	if input == "" {
		return nil
	}

	parts := strings.SplitAfter(input, "\n")
	if parts[len(parts)-1] == "" {
		return parts[:len(parts)-1]
	}

	return parts
}

func unifiedDiffTooLarge(before, after []string) bool {
	if len(before) > unifiedDiffMaxLines || len(after) > unifiedDiffMaxLines {
		return true
	}
	if len(before) == 0 || len(after) == 0 {
		return false
	}
	return len(after) > unifiedDiffMaxCells/len(before)
}

func unifiedDiffTooLargeMessage(
	fromFile string,
	toFile string,
	beforeLines int,
	afterLines int,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n", fromFile)
	fmt.Fprintf(&b, "+++ %s\n", toFile)
	fmt.Fprintf(
		&b,
		"@@ diff omitted: comparable state too large (before_lines=%d after_lines=%d) @@\n",
		beforeLines,
		afterLines,
	)
	return b.String()
}

func unifiedDiffOperations(
	before []string,
	after []string,
) []unifiedDiffOp {
	lcs := make([][]int, len(before)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(after)+1)
	}

	for i := len(before) - 1; i >= 0; i-- {
		for j := len(after) - 1; j >= 0; j-- {
			if before[i] == after[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
				continue
			}
			if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
				continue
			}
			lcs[i][j] = lcs[i][j+1]
		}
	}

	ops := make([]unifiedDiffOp, 0, len(before)+len(after))
	i := 0
	j := 0
	for i < len(before) && j < len(after) {
		if before[i] == after[j] {
			ops = append(ops, unifiedDiffOp{kind: unifiedDiffEqual, line: before[i]})
			i++
			j++
			continue
		}
		if lcs[i+1][j] >= lcs[i][j+1] {
			ops = append(ops, unifiedDiffOp{kind: unifiedDiffDelete, line: before[i]})
			i++
			continue
		}
		ops = append(ops, unifiedDiffOp{kind: unifiedDiffInsert, line: after[j]})
		j++
	}

	for ; i < len(before); i++ {
		ops = append(ops, unifiedDiffOp{kind: unifiedDiffDelete, line: before[i]})
	}
	for ; j < len(after); j++ {
		ops = append(ops, unifiedDiffOp{kind: unifiedDiffInsert, line: after[j]})
	}

	return ops
}

type unifiedDiffHunk struct {
	start int
	end   int
}

func unifiedDiffHunks(ops []unifiedDiffOp, contextLines int) []unifiedDiffHunk {
	if len(ops) == 0 {
		return nil
	}

	if contextLines < 0 {
		contextLines = 0
	}

	var hunks []unifiedDiffHunk
	for i := 0; i < len(ops); {
		for i < len(ops) && ops[i].kind == unifiedDiffEqual {
			i++
		}
		if i >= len(ops) {
			break
		}

		start := max(0, i-contextLines)
		end := i
		trailingEquals := 0
		for end < len(ops) {
			if ops[end].kind == unifiedDiffEqual {
				trailingEquals++
			} else {
				trailingEquals = 0
			}
			end++
			if contextLines > 0 && trailingEquals >= contextLines {
				break
			}
		}

		hunks = append(hunks, unifiedDiffHunk{start: start, end: end})
		i = end
	}

	return hunks
}

func unifiedDiffRange(
	ops []unifiedDiffOp,
	start int,
	end int,
) (int, int, int, int) {
	oldStart := 1
	newStart := 1

	for _, op := range ops[:start] {
		if op.kind != unifiedDiffInsert {
			oldStart++
		}
		if op.kind != unifiedDiffDelete {
			newStart++
		}
	}

	oldCount := 0
	newCount := 0
	for _, op := range ops[start:end] {
		if op.kind != unifiedDiffInsert {
			oldCount++
		}
		if op.kind != unifiedDiffDelete {
			newCount++
		}
	}

	return oldStart, oldCount, newStart, newCount
}
