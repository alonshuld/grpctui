package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/config"
	"github.com/alonshuld/grpctui/internal/format"
)

// The config file's format version. The guarantee these tests pin is written
// out in docs/formats.md: a file without a version reads as the current one, a
// file from a later grpctui says so rather than failing on one of its keys, and
// nothing about a version changes what the rest of the file means.

func TestLoad_Version(t *testing.T) {
	tests := map[string]struct {
		body    string
		want    format.Version
		wantErr string
	}{
		"no version at all is the format this grpctui knows": {
			body: "target: localhost:50051\n",
			want: 0,
		},
		"the current version": {
			body: "version: 1\ntarget: localhost:50051\n",
			want: format.Current,
		},
		"a version from a later grpctui": {
			body:    "version: 2\ntarget: localhost:50051\n",
			wantErr: "written in format version 2 and this grpctui reads up to 1",
		},
		"a version that is not a number": {
			body:    "version: latest\n",
			wantErr: `version must be a whole number like 1, got "latest"`,
		},
		"a version below the first one": {
			body:    "version: 0\n",
			wantErr: "version must be at least 1",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, tt.body)

			cfg, err := config.Load(path)

			if tt.wantErr != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tt.wantErr)
				assert.ErrorContains(t, err, path, "the error has to name the file it is about")
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.Version)
			assert.Equal(t, format.Current, cfg.Version.Or())
			assert.Equal(t, "localhost:50051", cfg.Target,
				"the version key must not disturb the rest of the file")
		})
	}
}

// TestLoad_LaterVersionBeatsTheUnknownKey is the reason the version is read on
// a pass of its own. A file from a later grpctui is made of keys this one has
// never heard of; without the early check the user is told about the key rather
// than about the version, and goes looking for a typo they did not make.
func TestLoad_LaterVersionBeatsTheUnknownKey(t *testing.T) {
	path := writeConfig(t, "version: 99\ntarget: localhost:50051\nretries: 3\n")

	_, err := config.Load(path)

	require.Error(t, err)
	require.ErrorIs(t, err, format.ErrUnsupported)
	require.ErrorContains(t, err, "upgrade grpctui")
	assert.NotContains(t, err.Error(), "retries",
		"the unknown key is a symptom of the version, and naming it sends the reader after the wrong thing")
}

// TestLoad_UnknownKeyWithoutAVersionStillFails pins the other direction: the
// version pass must not have turned a typo into something that is quietly
// accepted. A config file with a misspelt key is still an error.
func TestLoad_UnknownKeyWithoutAVersionStillFails(t *testing.T) {
	path := writeConfig(t, "target: localhost:50051\ntheem: dark\n")

	_, err := config.Load(path)

	require.Error(t, err)
	require.ErrorContains(t, err, "theem")
	assert.NotErrorIs(t, err, format.ErrUnsupported)
}

// TestLoad_MalformedYAMLIsStillAParseError pins that the version pass, which
// cannot parse a broken file either, stays out of the way and lets the real
// decode report it.
func TestLoad_MalformedYAMLIsStillAParseError(t *testing.T) {
	path := writeConfig(t, "target: [unclosed\n")

	_, err := config.Load(path)

	require.Error(t, err)
	require.ErrorContains(t, err, "parse config")
	assert.NotErrorIs(t, err, format.ErrUnsupported)
}
