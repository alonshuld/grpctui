package panels

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClampIndex(t *testing.T) {
	tests := map[string]struct {
		i, n int
		want int
	}{
		"inside the list":       {2, 5, 2},
		"first":                 {0, 5, 0},
		"last":                  {4, 5, 4},
		"past the end clamps":   {9, 5, 4},
		"before the start":      {-3, 5, 0},
		"an empty list":         {3, 0, 0},
		"a negative list":       {0, -1, 0},
		"a list of one":         {7, 1, 0},
		"empty and negative":    {-2, 0, 0},
		"exactly one past":      {5, 5, 4},
		"the cursor stays put":  {3, 4, 3},
		"a list that shrank":    {8, 2, 1},
		"nothing to move to":    {-1, 0, 0},
		"the only valid answer": {0, 1, 0},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, clampIndex(tt.i, tt.n))
		})
	}
}

func TestClampWindow(t *testing.T) {
	tests := map[string]struct {
		target, offset, height, total int
		want                          int
	}{
		"everything fits":                {3, 0, 10, 5, 0},
		"exactly fits":                   {4, 0, 5, 5, 0},
		"cursor already on screen":       {4, 2, 4, 20, 2},
		"cursor above the window":        {1, 5, 4, 20, 1},
		"cursor below the window":        {9, 2, 4, 20, 6},
		"cursor at the last row":         {19, 0, 4, 20, 16},
		"offset past the end pulls back": {19, 40, 4, 20, 16},
		"no height":                      {5, 3, 0, 20, 0},
		"negative height":                {5, 3, -2, 20, 0},
		"a negative offset":              {5, -4, 4, 20, 2},
		"one row visible":                {7, 0, 1, 20, 7},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := clampWindow(tt.target, tt.offset, tt.height, tt.total)
			assert.Equal(t, tt.want, got)

			// Whatever it returns has to be a window the caller can actually
			// slice with, and one the target is inside.
			if tt.height > 0 && tt.total > 0 {
				assert.GreaterOrEqual(t, got, 0)
				assert.LessOrEqual(t, got, max(tt.total-tt.height, 0))
				if tt.target >= 0 && tt.target < tt.total {
					assert.GreaterOrEqual(t, tt.target, got, "the target scrolled off the top")
					assert.Less(t, tt.target, got+tt.height, "the target scrolled off the bottom")
				}
			}
		})
	}
}
