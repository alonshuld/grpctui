// Package diff compares two blocks of text line by line.
//
// It exists for one screen: the response panel showing what changed between
// this call's answer and the previous one for the same method. That is a
// narrower job than a general diff library, and it shapes everything here —
// the output is a flat slice of lines with an operation attached, ready to be
// styled and handed to a viewport, rather than a patch anybody could apply.
//
// It is a leaf package: nothing of grpctui's is imported by it, and it knows
// nothing about protobuf, gRPC or the terminal.
package diff

import (
	"slices"
	"strings"
)

// Op is what happened to one line.
type Op int

const (
	// Equal marks a line both sides share.
	Equal Op = iota

	// Delete marks a line only the older side has.
	//
	// A line that changed is a Delete followed by an [Insert]: pairing the two
	// into a "modified" line would need a second, character-level diff, and a
	// JSON body's changed line is usually rewritten wholesale anyway.
	Delete

	// Insert marks a line only the newer side has.
	Insert
)

// Line is one line of a diff.
type Line struct {
	Op Op

	// Text is the line itself, without any marker. The caller decides how to
	// render one — a gutter character, a colour, both — which is why the marker
	// is not baked in here.
	Text string
}

// Skip is a run of unchanged lines the caller chose not to show, reported by
// [Unified] so the panel can say how many were hidden rather than silently
// dropping them.
type Skip struct {
	// At is the index in the returned slice where the skipped run would have
	// been, and Lines how many were left out.
	At    int
	Lines int
}

// maxCells bounds the LCS table. A response body is usually tens of lines and
// occasionally thousands; past this the quadratic table costs more memory than
// the answer is worth, so the two sides are reported as wholly replaced
// instead. That is a true diff, just a coarse one, and it keeps a pathological
// pair of bodies from taking the process down.
const maxCells = 4 << 20

// Lines compares two blocks of text and reports what changed, oldest side
// first.
//
// The comparison is by whole line: a line is equal or it is not. That is the
// right grain for an indented JSON body, where a changed value rewrites its
// line and nothing else.
func Lines(before, after string) []Line {
	return compare(split(before), split(after))
}

// split breaks text into lines. A trailing newline does not become an empty
// last line — a body ending in one is the same body as one that does not, and
// a phantom line at the end would show up as a difference between them.
func split(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// compare diffs two line slices.
//
// The common prefix and suffix are peeled off before the table is built. Two
// responses from the same method usually differ in a field or two out of
// dozens, so this is not an optimisation at the margin: it is what keeps the
// table small enough to build at all in the ordinary case.
func compare(before, after []string) []Line {
	prefix := 0
	for prefix < len(before) && prefix < len(after) && before[prefix] == after[prefix] {
		prefix++
	}

	suffix := 0
	for suffix < len(before)-prefix && suffix < len(after)-prefix &&
		before[len(before)-1-suffix] == after[len(after)-1-suffix] {
		suffix++
	}

	trimmedBefore := before[prefix : len(before)-suffix]
	trimmedAfter := after[prefix : len(after)-suffix]

	lines := make([]Line, 0, len(before)+len(after))
	for _, text := range before[:prefix] {
		lines = append(lines, Line{Op: Equal, Text: text})
	}
	lines = append(lines, middle(trimmedBefore, trimmedAfter)...)
	for _, text := range before[len(before)-suffix:] {
		lines = append(lines, Line{Op: Equal, Text: text})
	}
	return lines
}

// middle diffs the part that is left once the shared ends are gone.
func middle(before, after []string) []Line {
	switch {
	case len(before) == 0 && len(after) == 0:
		return nil
	case len(before) == 0:
		return all(Insert, after)
	case len(after) == 0:
		return all(Delete, before)
	case len(before)*len(after) > maxCells:
		return append(all(Delete, before), all(Insert, after)...)
	}
	return walk(lcs(before, after), before, after)
}

func all(op Op, texts []string) []Line {
	lines := make([]Line, 0, len(texts))
	for _, text := range texts {
		lines = append(lines, Line{Op: op, Text: text})
	}
	return lines
}

// lcs builds the longest-common-subsequence table: table[i][j] is the length of
// the longest subsequence shared by before[i:] and after[j:].
//
// It is filled from the end backwards so that [walk] can read it forwards,
// which is the order the diff has to come out in.
func lcs(before, after []string) [][]int {
	table := make([][]int, len(before)+1)
	cells := make([]int, (len(before)+1)*(len(after)+1))
	for i := range table {
		table[i], cells = cells[:len(after)+1], cells[len(after)+1:]
	}

	for i, left := range slices.Backward(before) {
		for j, right := range slices.Backward(after) {
			if left == right {
				table[i][j] = table[i+1][j+1] + 1
				continue
			}
			table[i][j] = max(table[i+1][j], table[i][j+1])
		}
	}
	return table
}

// walk reads the table into a diff.
//
// Where the two branches are equally good it deletes before inserting, so that
// a changed line reads old-then-new — the order a reader expects, and the order
// a unified diff has always used.
func walk(table [][]int, before, after []string) []Line {
	lines := make([]Line, 0, len(before)+len(after))

	i, j := 0, 0
	for i < len(before) && j < len(after) {
		switch {
		case before[i] == after[j]:
			lines = append(lines, Line{Op: Equal, Text: before[i]})
			i++
			j++
		case table[i+1][j] >= table[i][j+1]:
			lines = append(lines, Line{Op: Delete, Text: before[i]})
			i++
		default:
			lines = append(lines, Line{Op: Insert, Text: after[j]})
			j++
		}
	}

	lines = append(lines, all(Delete, before[i:])...)
	return append(lines, all(Insert, after[j:])...)
}

// Unified is [Lines] with long runs of unchanged lines collapsed, keeping
// context lines either side of every change.
//
// A response body is mostly unchanged between two calls, and a diff that shows
// all of it is a diff you have to hunt through for the one line that moved.
// The skipped runs are reported rather than dropped silently: a panel that says
// "42 unchanged lines" is honest about what it is not showing.
//
// A context of zero shows changed lines alone. A negative one is read as zero.
func Unified(before, after string, context int) ([]Line, []Skip) {
	lines := Lines(before, after)
	context = max(context, 0)

	// A diff with no changes at all is shown whole rather than collapsed to
	// nothing: "these two responses are identical" is better said by the panel
	// than by an empty box.
	if !changed(lines) {
		return lines, nil
	}

	keep := make([]bool, len(lines))
	for i, line := range lines {
		if line.Op == Equal {
			continue
		}
		for j := max(i-context, 0); j <= min(i+context, len(lines)-1); j++ {
			keep[j] = true
		}
	}

	out := make([]Line, 0, len(lines))
	var skips []Skip
	for i := 0; i < len(lines); {
		if keep[i] {
			out = append(out, lines[i])
			i++
			continue
		}

		run := 0
		for i+run < len(lines) && !keep[i+run] {
			run++
		}
		skips = append(skips, Skip{At: len(out), Lines: run})
		i += run
	}
	return out, skips
}

// changed reports whether a diff has anything in it beyond equal lines.
func changed(lines []Line) bool {
	for _, line := range lines {
		if line.Op != Equal {
			return true
		}
	}
	return false
}
