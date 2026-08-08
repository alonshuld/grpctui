package main

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/config"
	"github.com/alonshuld/grpctui/internal/render"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

func TestThemes(t *testing.T) {
	custom := config.Config{
		ThemeName: "midnight",
		Themes: []config.Theme{
			{Name: "midnight", Base: styles.ThemeDark, Colors: map[string]string{"primary": "#ff00ff"}},
		},
	}

	tests := map[string]struct {
		cfg      config.Config
		opts     options
		wantName string
	}{
		"nothing configured is the default": {
			wantName: styles.ThemeAuto,
		},
		"the config file names one": {
			cfg:      config.Config{ThemeName: styles.ThemeLight},
			wantName: styles.ThemeLight,
		},
		"a custom theme": {
			cfg:      custom,
			wantName: "midnight",
		},
		// The flag wins over the file, as it does everywhere else: it is how
		// somebody whose terminal reports its background wrongly gets through one
		// session without editing anything.
		"the flag wins over the file": {
			cfg:      custom,
			opts:     options{theme: styles.ThemeDark},
			wantName: styles.ThemeDark,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := themes(tt.cfg, tt.opts)
			require.NoError(t, err)
			assert.Equal(t, tt.wantName, got.theme().Name)
			assert.GreaterOrEqual(t, len(got.list), 3)
		})
	}
}

func TestThemes_Errors(t *testing.T) {
	tests := map[string]struct {
		cfg  config.Config
		opts options
		want string
	}{
		"a theme nothing defines": {
			cfg:  config.Config{ThemeName: "solarised"},
			want: "solarised",
		},
		"a flag naming a theme nothing defines": {
			opts: options{theme: "solarised"},
			want: "solarised",
		},
		"a colour that will not parse": {
			cfg: config.Config{Themes: []config.Theme{
				{Name: "x", Colors: map[string]string{"primary": "beige"}},
			}},
			want: "beige",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := themes(tt.cfg, tt.opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// TestThemes_CustomColoursSurvive pins the whole point of the conversion: a
// colour written as a string in YAML has to reach lipgloss as a colour.
func TestThemes_CustomColoursSurvive(t *testing.T) {
	got, err := themes(config.Config{
		ThemeName: "midnight",
		Themes: []config.Theme{{
			Name:   "midnight",
			Base:   styles.ThemeDark,
			Colors: map[string]string{"primary": "#ff00ff", "error": "0/15"},
		}},
	}, options{})
	require.NoError(t, err)

	palette := got.theme().Palette
	assert.Equal(t, lipgloss.Color("#ff00ff"), palette.Primary)
	assert.Equal(t, lipgloss.AdaptiveColor{Light: "0", Dark: "15"}, palette.Error)

	// Anything unmentioned came from the base rather than the default.
	assert.Equal(t, styles.BuiltinThemes()[1].Palette.Success, palette.Success)
}

func TestLook(t *testing.T) {
	got, err := look(config.Config{
		ThemeName: styles.ThemeDark,
		Keys:      map[string]string{"send": "ctrl+g"},
		Renderers: map[string]bool{render.RendererDuration: false},
	}, options{})
	require.NoError(t, err)

	assert.Equal(t, styles.ThemeDark, got.palettes.theme().Name)
	assert.Equal(t, []string{"ctrl+g"}, got.keys.Send.Keys())
	assert.Equal(t, []string{render.RendererTimestamp}, got.renderers.Names())
}

func TestLook_Errors(t *testing.T) {
	tests := map[string]config.Config{
		"a theme that will not resolve": {ThemeName: "solarised"},
		"an action that is not one":     {Keys: map[string]string{"snd": "ctrl+g"}},
		"a keybinding conflict":         {Keys: map[string]string{"send": "q"}},
		"a renderer that is not one":    {Renderers: map[string]bool{"clock": false}},
	}

	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := look(cfg, options{})
			require.Error(t, err)
		})
	}
}

func TestSpecs(t *testing.T) {
	assert.Nil(t, specs(nil))

	got := specs([]config.Theme{
		{Name: "a", Base: "dark", Colors: map[string]string{"primary": "1"}},
		{Name: "b"},
	})
	assert.Equal(t, []styles.Spec{
		{Name: "a", Base: "dark", Colors: map[string]string{"primary": "1"}},
		{Name: "b"},
	}, got)
}

// TestPalettes_ThemeOutOfRange pins the fallback: an index nothing points at
// yields the built-in default rather than a panic.
func TestPalettes_ThemeOutOfRange(t *testing.T) {
	assert.Equal(t, styles.DefaultTheme(), palettes{}.theme())
	assert.Equal(t, styles.DefaultTheme(), palettes{list: styles.BuiltinThemes(), active: 9}.theme())
}
