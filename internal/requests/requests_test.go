package requests_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/requests"
)

func TestRequest_Names(t *testing.T) {
	r := requests.Request{Method: "helloworld.Greeter.SayHello"}

	assert.Equal(t, "SayHello", r.ShortMethod())
	assert.Equal(t, "helloworld.Greeter", r.Service())
	assert.Equal(t, "SayHello", r.Label(), "a history entry has no name, so it is called by its method")

	r.Name = "greet alice"
	assert.Equal(t, "greet alice", r.Label())

	t.Run("a method with no package", func(t *testing.T) {
		bare := requests.Request{Method: "SayHello"}
		assert.Equal(t, "SayHello", bare.ShortMethod())
		assert.Empty(t, bare.Service())
	})
}

func TestRequest_Search(t *testing.T) {
	r := requests.Request{
		Name:   "Greet Alice",
		Method: "helloworld.Greeter.SayHello",
		Body: map[string]any{
			"name":  "Alice",
			"count": 7,
			"tags":  []any{"beta", "eu-west"},
			"nested": map[string]any{
				"note": "Second Level",
			},
			"flag":  true,
			"ratio": 1.5,
			"empty": nil,
		},
	}

	haystack := r.Search()
	for _, want := range []string{
		"greet alice", "helloworld.greeter.sayhello", "alice", "7",
		"beta", "eu-west", "nested", "second level", "true", "1.5",
	} {
		assert.Contains(t, haystack, want)
	}
}

func TestMatcher(t *testing.T) {
	r := requests.Request{
		Method: "helloworld.Greeter.SayHello",
		Body:   map[string]any{"name": "alice"},
	}
	haystack := r.Search()

	cases := map[string]bool{
		"":                true,
		"   ":             true,
		"sayhello":        true,
		"SAYHELLO":        true,
		"alice":           true,
		"sayhello alice":  true,
		"alice sayhello":  true,
		"sayhello bob":    false,
		"goodbye":         false,
		"greeter.sayhell": true,
	}

	for query, want := range cases {
		t.Run(query, func(t *testing.T) {
			assert.Equal(t, want, requests.NewMatcher(query).Match(haystack))
		})
	}

	require.True(t, requests.NewMatcher("").Empty())
	require.False(t, requests.NewMatcher("a").Empty())
}

// A body arrives as whatever the decoder produced: encoding/json gives float64
// for every number, YAML gives int for a small one and int64 or uint64 for a
// large one, and a hand-edited file can hold something stranger still. None of
// it may fall out of the search index.
func TestRequest_SearchCoversEveryScalar(t *testing.T) {
	r := requests.Request{
		Method: "a.B",
		Body: map[string]any{
			"small":  42,
			"big":    int64(9007199254740993),
			"huge":   uint64(18446744073709551615),
			"ratio":  2.25,
			"flag":   false,
			"odd":    struct{ Name string }{Name: "surprise"},
			"absent": nil,
		},
	}

	haystack := r.Search()
	for _, want := range []string{
		"42", "9007199254740993", "18446744073709551615", "2.25", "false", "surprise",
	} {
		assert.Contains(t, haystack, want)
	}
}
