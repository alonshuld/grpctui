package vars_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/vars"
)

func TestSetResolve(t *testing.T) {
	set := vars.NewSet([]vars.Variable{
		{Name: "user_id", Value: "42"},
		{Name: "host", Value: "staging.example.com"},
		{Name: "empty", Value: ""},
	})

	tests := []struct {
		name    string
		in      string
		want    string
		missing []string
	}{
		{name: "no reference", in: "alice", want: "alice"},
		{name: "whole value", in: "{{user_id}}", want: "42"},
		{name: "embedded", in: "https://{{host}}/v1", want: "https://staging.example.com/v1"},
		{name: "twice", in: "{{user_id}}-{{user_id}}", want: "42-42"},
		{name: "spaces inside the braces", in: "{{ user_id }}", want: "42"},
		{name: "bound to nothing at all", in: "[{{empty}}]", want: "[]"},
		{name: "a single brace is not a reference", in: "{user_id}", want: "{user_id}"},
		{name: "an unclosed reference is text", in: "{{user_id", want: "{{user_id"},
		{name: "json survives", in: `{"a": 1}`, want: `{"a": 1}`},
		{
			name:    "unset is reported and left as written",
			in:      "{{nope}}",
			want:    "{{nope}}",
			missing: []string{"nope"},
		},
		{
			name:    "every unset name is reported at once",
			in:      "{{nope}}/{{user_id}}/{{also}}",
			want:    "{{nope}}/42/{{also}}",
			missing: []string{"nope", "also"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := set.Resolve(tt.in)
			assert.Equal(t, tt.want, got)

			if tt.missing == nil {
				assert.NoError(t, err)
				return
			}

			var unset *vars.UnsetError
			require.ErrorAs(t, err, &unset)
			assert.Equal(t, tt.missing, unset.Names)
		})
	}
}

// The zero Set is what a run with no environments has, and a request without
// references must not notice it.
func TestZeroSetResolves(t *testing.T) {
	var set vars.Set

	got, err := set.Resolve("alice")
	require.NoError(t, err)
	assert.Equal(t, "alice", got)
	assert.Equal(t, 0, set.Len())

	got, err = set.Resolve("{{nope}}")
	assert.Equal(t, "{{nope}}", got)
	assert.Error(t, err)
}

func TestRefers(t *testing.T) {
	for in, want := range map[string]bool{
		"":             false,
		"alice":        false,
		"{{a}}":        true,
		"x{{ a }}y":    true,
		"{a}":          false,
		"{{}}":         false,
		"{{1abc}}":     false,
		"{{a.b}}":      false,
		`{"k": "v"}`:   false,
		"{{a}}{{b}}":   true,
		"}}{{":         false,
		"{{_private}}": true,
	} {
		t.Run(in, func(t *testing.T) {
			assert.Equal(t, want, vars.Refers(in))
		})
	}
}

func TestReferences(t *testing.T) {
	assert.Equal(t, []string{"a", "b"}, vars.References("{{a}}/{{b}}/{{a}}"))
	assert.Empty(t, vars.References("nothing here"))
}

func TestSetIsAValue(t *testing.T) {
	before := vars.NewSet([]vars.Variable{{Name: "token", Value: "one"}})
	after := before.With(vars.Variable{Name: "token", Value: "two", Captured: true})

	got, ok := before.Lookup("token")
	require.True(t, ok)
	assert.Equal(t, "one", got, "the earlier set saw a later binding")

	got, ok = after.Lookup("token")
	require.True(t, ok)
	assert.Equal(t, "two", got)
	assert.Equal(t, 1, after.Len(), "rebinding a name grew the set")

	// Removing likewise leaves whoever still holds the old set alone.
	empty := after.Without("token")
	assert.Equal(t, 0, empty.Len())
	assert.Equal(t, 1, after.Len())
	assert.Equal(t, after, after.Without("never bound"))
}

func TestSetIsOrdered(t *testing.T) {
	set := vars.NewSet([]vars.Variable{
		{Name: "zulu"}, {Name: "alpha"}, {Name: "mike"},
	})
	assert.Equal(t, []string{"alpha", "mike", "zulu"}, set.Names())

	set = set.With(vars.Variable{Name: "bravo"})
	assert.Equal(t, []string{"alpha", "bravo", "mike", "zulu"}, set.Names())
}

func TestNewSetKeepsTheLastDuplicate(t *testing.T) {
	set := vars.NewSet([]vars.Variable{
		{Name: "a", Value: "first"},
		{Name: "a", Value: "second"},
	})

	require.Equal(t, 1, set.Len())
	got, _ := set.Lookup("a")
	assert.Equal(t, "second", got)
}

func TestValidateName(t *testing.T) {
	for _, name := range []string{"a", "_a", "user_id", "A1"} {
		t.Run("ok/"+name, func(t *testing.T) {
			assert.NoError(t, vars.ValidateName(name))
		})
	}
	for _, name := range []string{"", "1a", "a b", "a-b", "a.b", "{{a}}", "a=b"} {
		t.Run("bad/"+name, func(t *testing.T) {
			assert.Error(t, vars.ValidateName(name))
		})
	}
}

func TestSplitAssignment(t *testing.T) {
	tests := []struct {
		in    string
		name  string
		value string
		ok    bool
	}{
		{in: "a=b", name: "a", value: "b", ok: true},
		{in: " a = b ", name: "a", value: " b ", ok: true},
		{in: "a=", name: "a", ok: true},
		{in: "url=https://x/?q=1", name: "url", value: "https://x/?q=1", ok: true},
		{in: "a"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			name, value, ok := vars.SplitAssignment(tt.in)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.name, name)
			assert.Equal(t, tt.value, value)
		})
	}
}

func TestEnvironmentSet(t *testing.T) {
	env := vars.Environment{
		Name:      "dev",
		Target:    "localhost:50051",
		Variables: []vars.Variable{{Name: "user_id", Value: "1"}},
	}

	got, err := env.Set().Resolve("{{user_id}}")
	require.NoError(t, err)
	assert.Equal(t, "1", got)
	assert.Equal(t, "dev", env.Label())
}
