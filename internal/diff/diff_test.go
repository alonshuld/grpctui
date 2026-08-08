package diff_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/diff"
)

// render writes a diff back out in the shape a reader recognises, so that a
// failing case names the lines rather than a slice of structs.
func render(lines []diff.Line) string {
	var b strings.Builder
	for _, line := range lines {
		switch line.Op {
		case diff.Delete:
			b.WriteString("-")
		case diff.Insert:
			b.WriteString("+")
		default:
			b.WriteString(" ")
		}
		b.WriteString(line.Text)
		b.WriteString("\n")
	}
	return b.String()
}

func TestLines(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
		want string
	}{
		{
			name: "identical",
			old:  "a\nb\nc",
			new:  "a\nb\nc",
			want: " a\n b\n c\n",
		},
		{
			name: "one line changed",
			old:  "a\nb\nc",
			new:  "a\nB\nc",
			want: " a\n-b\n+B\n c\n",
		},
		{
			name: "line added at the end",
			old:  "a\nb",
			new:  "a\nb\nc",
			want: " a\n b\n+c\n",
		},
		{
			name: "line removed from the middle",
			old:  "a\nb\nc",
			new:  "a\nc",
			want: " a\n-b\n c\n",
		},
		{
			name: "both sides empty",
			old:  "",
			new:  "",
			want: "",
		},
		{
			name: "everything new",
			old:  "",
			new:  "a\nb",
			want: "+a\n+b\n",
		},
		{
			name: "everything gone",
			old:  "a\nb",
			new:  "",
			want: "-a\n-b\n",
		},
		{
			name: "nothing in common",
			old:  "a\nb",
			new:  "x\ny",
			want: "-a\n-b\n+x\n+y\n",
		},
		{
			name: "a trailing newline is not a line",
			old:  "a\nb\n",
			new:  "a\nb",
			want: " a\n b\n",
		},
		{
			name: "an interior blank line is",
			old:  "a\n\nb",
			new:  "a\nb",
			want: " a\n-\n b\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, render(diff.Lines(tt.old, tt.new)))
		})
	}
}

// The old side must come out before the new one wherever both are possible, or
// a changed line reads backwards.
func TestLinesDeletesBeforeInserting(t *testing.T) {
	lines := diff.Lines("one\ntwo", "uno\ndos")

	require.Len(t, lines, 4)
	assert.Equal(t, diff.Delete, lines[0].Op)
	assert.Equal(t, diff.Delete, lines[1].Op)
	assert.Equal(t, diff.Insert, lines[2].Op)
	assert.Equal(t, diff.Insert, lines[3].Op)
}

// Every line of both inputs has to appear exactly once on its own side, or the
// diff has quietly lost part of a response.
func TestLinesKeepsEveryLine(t *testing.T) {
	const old = "a\nb\nc\nd\ne"
	const after = "a\nc\nx\nd\ne\nf"

	var gotOld, gotNew []string
	for _, line := range diff.Lines(old, after) {
		switch line.Op {
		case diff.Delete:
			gotOld = append(gotOld, line.Text)
		case diff.Insert:
			gotNew = append(gotNew, line.Text)
		default:
			gotOld = append(gotOld, line.Text)
			gotNew = append(gotNew, line.Text)
		}
	}

	assert.Equal(t, strings.Split(old, "\n"), gotOld)
	assert.Equal(t, strings.Split(after, "\n"), gotNew)
}

// Past the table's budget the two sides are reported as wholly replaced. That
// is still a correct diff, and it is what keeps a pair of enormous bodies from
// allocating gigabytes.
func TestLinesFallsBackOnHugeInputs(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two 4000-line bodies")
	}

	const n = 4000
	old := make([]string, n)
	after := make([]string, n)
	for i := range n {
		old[i] = "old " + strconv.Itoa(i)
		after[i] = "new " + strconv.Itoa(i)
	}

	lines := diff.Lines(strings.Join(old, "\n"), strings.Join(after, "\n"))
	require.Len(t, lines, 2*n)
	assert.Equal(t, diff.Delete, lines[0].Op)
	assert.Equal(t, diff.Insert, lines[n].Op)
}

func TestUnified(t *testing.T) {
	old := strings.Join([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}, "\n")
	after := strings.Join([]string{"1", "2", "3", "4", "X", "6", "7", "8", "9"}, "\n")

	lines, skips := diff.Unified(old, after, 1)

	assert.Equal(t, " 4\n-5\n+X\n 6\n", render(lines))
	require.Len(t, skips, 2)
	assert.Equal(t, diff.Skip{At: 0, Lines: 3}, skips[0], "the lines before the context")
	assert.Equal(t, diff.Skip{At: 4, Lines: 3}, skips[1], "the lines after it")
}

func TestUnifiedWithoutContext(t *testing.T) {
	lines, skips := diff.Unified("a\nb\nc", "a\nB\nc", 0)

	assert.Equal(t, "-b\n+B\n", render(lines))
	require.Len(t, skips, 2)
	assert.Equal(t, 1, skips[0].Lines)
	assert.Equal(t, 1, skips[1].Lines)
}

// Two identical bodies are shown whole. Collapsing them to nothing would leave
// the panel with an empty box where "these are the same" is the answer.
func TestUnifiedKeepsAnUnchangedBody(t *testing.T) {
	lines, skips := diff.Unified("a\nb\nc", "a\nb\nc", 1)

	assert.Equal(t, " a\n b\n c\n", render(lines))
	assert.Empty(t, skips)
}

func TestUnifiedNegativeContext(t *testing.T) {
	lines, _ := diff.Unified("a\nb\nc", "a\nB\nc", -3)
	assert.Equal(t, "-b\n+B\n", render(lines))
}

// A skip's At indexes the returned slice, so a panel can splice its "n
// unchanged lines" marker in without recounting.
func TestUnifiedSkipsIndexTheOutput(t *testing.T) {
	old := strings.Join([]string{"a", "1", "2", "3", "b", "4", "5", "6", "c"}, "\n")
	after := strings.Join([]string{"A", "1", "2", "3", "B", "4", "5", "6", "C"}, "\n")

	lines, skips := diff.Unified(old, after, 0)
	require.Len(t, skips, 2)

	for _, skip := range skips {
		require.LessOrEqual(t, skip.At, len(lines))
	}
	assert.Equal(t, 2, skips[0].At, "after the first delete/insert pair")
	assert.Equal(t, 4, skips[1].At)
}
