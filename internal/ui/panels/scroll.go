package panels

// The tree and the request form scroll the same way: a cursor that moves within
// a list, and a window that follows it. They differ only in what they are
// counting — the tree's rows are its lines, while the form's cursor indexes
// fields and its window counts rendered lines, of which a field may own several.
// The arithmetic underneath is the same, and keeping one copy of it is what
// stops a fix landing in one panel and not the other.

// clampIndex clamps a cursor index into a list of n items.
//
// An empty list leaves the cursor at zero rather than at -1, so that a panel
// with nothing in it still has a cursor its render can start from.
func clampIndex(i, n int) int {
	if n <= 0 || i < 0 {
		return 0
	}
	return min(i, n-1)
}

// clampWindow returns the scroll offset that brings target into a window of
// height rows over a list of total rows, moving as little as possible: a cursor
// already on screen leaves the window where it is.
//
// A list that fits, or a window with no height, scrolls to the top — there is
// nothing to reveal.
func clampWindow(target, offset, height, total int) int {
	if height <= 0 || total <= height {
		return 0
	}

	if target < offset {
		offset = target
	}
	if target >= offset+height {
		offset = target - height + 1
	}
	return max(min(offset, total-height), 0)
}
