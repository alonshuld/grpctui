package format_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/alonshuld/grpctui/internal/format"
)

func TestVersionCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version format.Version
		wantErr string
	}{
		{
			name:    "absent is the current version",
			version: 0,
		},
		{
			name:    "the current version",
			version: format.Current,
		},
		{
			name:    "the oldest readable version",
			version: format.First,
		},
		{
			name:    "a version from a later grpctui",
			version: format.Current + 1,
			wantErr: "written in format version 2 and this grpctui reads up to 1: upgrade grpctui",
		},
		{
			name:    "a negative version",
			version: -3,
			wantErr: "claims format version -3, which is not a version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.version.Check("/home/x/.config/grpctui/config.yaml")
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			require.ErrorIs(t, err, format.ErrUnsupported)
			require.ErrorContains(t, err, tt.wantErr)
			assert.ErrorContains(t, err, "/home/x/.config/grpctui/config.yaml",
				"the error has to name the file: a user with a config file and three collections needs to know which one to edit")
		})
	}
}

// TestVersionCheckUnsupportedError pins the two fields a caller might want off
// the error, since the message is prose and the numbers are not.
func TestVersionCheckUnsupportedError(t *testing.T) {
	t.Parallel()

	err := (format.Current + 5).Check("smoke.yaml")

	var unsupported *format.UnsupportedError
	require.ErrorAs(t, err, &unsupported)
	assert.Equal(t, "smoke.yaml", unsupported.Path)
	assert.Equal(t, format.Current+5, unsupported.Have)
	assert.Equal(t, format.Current, unsupported.Known)
}

func TestVersionOr(t *testing.T) {
	t.Parallel()

	assert.True(t, format.Version(0).Absent())
	assert.Equal(t, format.Current, format.Version(0).Or())
	assert.False(t, format.Current.Absent())
	assert.Equal(t, format.Current, format.Current.Or())
}

func TestVersionUnmarshalYAML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yaml    string
		want    format.Version
		wantErr string
	}{
		{
			name: "a missing key leaves the version absent",
			yaml: "other: 1\n",
			want: 0,
		},
		{
			name: "a whole number",
			yaml: "version: 1\n",
			want: 1,
		},
		{
			name: "a number this grpctui does not know is still decoded",
			yaml: "version: 9\n",
			want: 9,
		},
		{
			name:    "a word",
			yaml:    "version: one\n",
			wantErr: `version must be a whole number like 1, got "one"`,
		},
		{
			name:    "a written-down zero",
			yaml:    "version: 0\n",
			wantErr: "version must be at least 1, got 0",
		},
		{
			// The line somebody started and did not finish. It is absent rather
			// than a zero: refusing it would refuse the file over a number the
			// user never typed.
			name: "a key with nothing after it is absent",
			yaml: "version:\nother: 2\n",
			want: 0,
		},
		{
			name: "an explicit null is absent too",
			yaml: "version: ~\n",
			want: 0,
		},
		{
			name:    "a block of keys",
			yaml:    "version:\n  major: 1\n",
			wantErr: "version must be a whole number like 1, got a block of keys",
		},
		{
			name:    "a list",
			yaml:    "version:\n  - 1\n",
			wantErr: "version must be a whole number like 1, got a list",
		},
		{
			// yaml would decode this into an int by truncating it, and a 1.5 read
			// as 1 is a compatibility claim nobody made.
			name:    "a number that is not whole",
			yaml:    "version: 1.5\n",
			wantErr: `version must be a whole number like 1, got "1.5"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var file struct {
				Version format.Version `yaml:"version"`
				Other   int            `yaml:"other"`
			}
			err := yaml.Unmarshal([]byte(tt.yaml), &file)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, file.Version)
		})
	}
}

// TestCurrentIsWithinTheWindow is a guard on the constants themselves: a
// release that bumps Current without deciding what the oldest readable version
// is has silently dropped the compatibility promise this package exists to
// make.
func TestCurrentIsWithinTheWindow(t *testing.T) {
	t.Parallel()

	assert.GreaterOrEqual(t, format.Current, format.First)
	assert.Positive(t, int(format.First))
}

// TestErrUnsupportedDoesNotMatchOtherErrors pins the other direction of Is: a
// malformed-YAML error must not be reported as a version problem, or the advice
// in the message ("upgrade grpctui") is advice for the wrong bug.
func TestErrUnsupportedDoesNotMatchOtherErrors(t *testing.T) {
	t.Parallel()

	assert.NotErrorIs(t, errors.New("parse: line 3: mapping values are not allowed"), format.ErrUnsupported)
}
