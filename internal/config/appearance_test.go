package config_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/config"
)

// This file covers the three things v0.9 added to the config file: themes,
// keybinding remapping and the .proto-file fallback.

func TestLoad_Theme(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, `
theme: midnight
themes:
  - name: midnight
    base: dark
    colors:
      primary: "#ff00ff"
      border: "0/15"
`))
	require.NoError(t, err)

	assert.Equal(t, "midnight", cfg.ThemeName)
	require.Len(t, cfg.Themes, 1)
	assert.Equal(t, "midnight", cfg.Themes[0].Name)
	assert.Equal(t, "dark", cfg.Themes[0].Base)
	assert.Equal(t, map[string]string{"primary": "#ff00ff", "border": "0/15"}, cfg.Themes[0].Colors)
}

// TestLoad_ThemeNames pins the same rule profiles and environments follow: a
// duplicate makes --theme ambiguous, and one of the two would be unreachable.
func TestLoad_ThemeNames(t *testing.T) {
	tests := map[string]struct {
		body string
		want string
	}{
		"an unnamed theme": {
			body: "themes:\n  - colors: {primary: \"1\"}\n",
			want: "theme 1 has no name",
		},
		"a duplicate name": {
			body: "themes:\n  - name: mine\n  - name: mine\n",
			want: `theme "mine" is defined twice`,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(writeConfig(t, tt.body))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestLoad_Keys(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, `
keys:
  send: ctrl+g
  traffic: ""
`))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"send": "ctrl+g", "traffic": ""}, cfg.Keys)
}

func TestLoad_Renderers(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, `
renderers:
  timestamp: false
  duration: true
`))
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"timestamp": false, "duration": true}, cfg.Renderers)
}

func TestLoad_Proto(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, `
proto:
  files:
    - api/v1/greeter.proto
  import_paths:
    - api
`))
	require.NoError(t, err)
	assert.Equal(t, []string{"api/v1/greeter.proto"}, cfg.Proto.Files)
	assert.Equal(t, []string{"api"}, cfg.Proto.ImportPaths)
}

func TestProto_Paths(t *testing.T) {
	t.Setenv("GRPCTUI_TEST_PROTO_DIR", "/srv/protos")
	t.Setenv("HOME", "/home/tester")

	proto := config.Proto{
		Files:       []string{"${GRPCTUI_TEST_PROTO_DIR}/api.proto", "~/other.proto"},
		ImportPaths: []string{"~", "${GRPCTUI_TEST_PROTO_DIR}"},
	}

	files, imports, err := proto.Paths()
	require.NoError(t, err)
	assert.Equal(t, []string{"/srv/protos/api.proto", filepath.Join("/home/tester", "other.proto")}, files)
	assert.Equal(t, []string{"/home/tester", "/srv/protos"}, imports)
}

// TestProto_Paths_MissingVariable pins that a ${VAR} nobody exported is
// reported rather than becoming the empty string — the same rule every other
// path in this file follows.
func TestProto_Paths_MissingVariable(t *testing.T) {
	proto := config.Proto{Files: []string{"${GRPCTUI_TEST_NOT_SET}/api.proto"}}

	_, _, err := proto.Paths()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GRPCTUI_TEST_NOT_SET")
}

// TestProto_Paths_ReportsEveryProblemAtOnce pins that a file missing three
// variables says so once rather than over three runs.
func TestProto_Paths_ReportsEveryProblemAtOnce(t *testing.T) {
	proto := config.Proto{
		Files:       []string{"${GRPCTUI_TEST_A}/a.proto"},
		ImportPaths: []string{"${GRPCTUI_TEST_B}"},
	}

	_, _, err := proto.Paths()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GRPCTUI_TEST_A")
	assert.Contains(t, err.Error(), "GRPCTUI_TEST_B")
}

func TestProto_Paths_Empty(t *testing.T) {
	files, imports, err := config.Proto{}.Paths()
	require.NoError(t, err)
	assert.Nil(t, files)
	assert.Nil(t, imports)
}

// TestLoad_UnknownKeysStillRejected pins that adding four keys to the file did
// not loosen the rule that a typo is an error.
func TestLoad_UnknownKeysStillRejected(t *testing.T) {
	tests := []string{
		"thme: dark\n",
		"themes:\n  - name: x\n    colours: {primary: \"1\"}\n",
		"proto:\n  file: a.proto\n",
	}

	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			_, err := config.Load(writeConfig(t, body))
			require.Error(t, err)
		})
	}
}
