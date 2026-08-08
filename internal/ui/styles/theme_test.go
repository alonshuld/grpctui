package styles_test

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/ui/styles"
)

func TestBuiltinThemes(t *testing.T) {
	t.Parallel()

	themes := styles.BuiltinThemes()
	require.Len(t, themes, 3)

	var names []string
	for _, theme := range themes {
		names = append(names, theme.Name)
		assert.NotEmpty(t, theme.Description, "%s has no description", theme.Name)
	}
	assert.Equal(t, []string{styles.ThemeAuto, styles.ThemeDark, styles.ThemeLight}, names)
}

// TestBuiltinThemes_Fixed pins the point of the dark and light themes: they are
// the auto theme with the guessing taken out, so each colour is one half of the
// adaptive pair rather than the pair itself.
func TestBuiltinThemes_Fixed(t *testing.T) {
	t.Parallel()

	auto := styles.DefaultPalette()
	adaptive, ok := auto.Primary.(lipgloss.AdaptiveColor)
	require.True(t, ok, "the default palette should be adaptive")

	themes := styles.BuiltinThemes()
	dark, light := themes[1].Palette, themes[2].Palette

	assert.Equal(t, lipgloss.Color(adaptive.Dark), dark.Primary)
	assert.Equal(t, lipgloss.Color(adaptive.Light), light.Primary)

	// Nothing adaptive survives in either, or the theme would still be guessing.
	for _, c := range []lipgloss.TerminalColor{
		dark.Primary, dark.Secondary, dark.Muted, dark.Border,
		dark.Error, dark.Success, dark.Text, dark.Inverted,
		light.Primary, light.Secondary, light.Muted, light.Border,
		light.Error, light.Success, light.Text, light.Inverted,
	} {
		_, adaptive := c.(lipgloss.AdaptiveColor)
		assert.False(t, adaptive, "%v is still adaptive", c)
	}
}

func TestParseColor(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in   string
		want lipgloss.TerminalColor
	}{
		"a six-digit hex":     {in: "#7D56F4", want: lipgloss.Color("#7D56F4")},
		"a three-digit hex":   {in: "#b9f", want: lipgloss.Color("#b9f")},
		"an ANSI index":       {in: "5", want: lipgloss.Color("5")},
		"the top ANSI index":  {in: "255", want: lipgloss.Color("255")},
		"the bottom one":      {in: "0", want: lipgloss.Color("0")},
		"surrounding spaces":  {in: "  #7D56F4  ", want: lipgloss.Color("#7D56F4")},
		"an adaptive pair":    {in: "#000000/#ffffff", want: lipgloss.AdaptiveColor{Light: "#000000", Dark: "#ffffff"}},
		"an adaptive of ANSI": {in: "0/15", want: lipgloss.AdaptiveColor{Light: "0", Dark: "15"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := styles.ParseColor(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseColor_Errors(t *testing.T) {
	t.Parallel()

	// An unrecognised string is refused rather than rendered as the default
	// foreground: a theme that half worked would be harder to debug than one
	// that would not load.
	for _, in := range []string{"", "  ", "red", "#12", "#1234567", "256", "-1", "#fff/", "/#fff", "#fff/nope"} {
		t.Run(in, func(t *testing.T) {
			t.Parallel()

			_, err := styles.ParseColor(in)
			require.Error(t, err, "%q should not parse", in)
		})
	}
}

func TestCatalog(t *testing.T) {
	t.Parallel()

	themes, err := styles.Catalog([]styles.Spec{
		{Name: "midnight", Base: styles.ThemeDark, Colors: map[string]string{"primary": "#ff00ff"}},
	})
	require.NoError(t, err)
	require.Len(t, themes, 4)

	custom := themes[3]
	assert.Equal(t, "midnight", custom.Name)
	assert.Equal(t, lipgloss.Color("#ff00ff"), custom.Palette.Primary)

	// Everything unmentioned came from the base rather than from the default.
	assert.Equal(t, styles.BuiltinThemes()[1].Palette.Border, custom.Palette.Border)
	assert.Contains(t, custom.Description, styles.ThemeDark)
}

// TestCatalog_ReplacesABuiltin pins that taking a built-in's name adjusts it in
// place. Somebody who dislikes one colour of the dark theme should not end up
// with two entries called "dark" in the switcher.
func TestCatalog_ReplacesABuiltin(t *testing.T) {
	t.Parallel()

	themes, err := styles.Catalog([]styles.Spec{
		{Name: styles.ThemeDark, Base: styles.ThemeDark, Colors: map[string]string{"error": "9"}},
	})
	require.NoError(t, err)
	require.Len(t, themes, 3)

	assert.Equal(t, styles.ThemeDark, themes[1].Name)
	assert.Equal(t, lipgloss.Color("9"), themes[1].Palette.Error)
	assert.Equal(t, styles.BuiltinThemes()[1].Palette.Success, themes[1].Palette.Success)
}

// TestCatalog_BaseOnAnEarlierCustom pins that a file can define a palette once
// and vary it twice.
func TestCatalog_BaseOnAnEarlierCustom(t *testing.T) {
	t.Parallel()

	themes, err := styles.Catalog([]styles.Spec{
		{Name: "base", Colors: map[string]string{"primary": "#111111"}},
		{Name: "variant", Base: "base", Colors: map[string]string{"error": "#222222"}},
	})
	require.NoError(t, err)
	require.Len(t, themes, 5)

	variant := themes[4]
	assert.Equal(t, lipgloss.Color("#111111"), variant.Palette.Primary)
	assert.Equal(t, lipgloss.Color("#222222"), variant.Palette.Error)
}

func TestCatalog_Errors(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		specs []styles.Spec
		want  []string
	}{
		"no name": {
			specs: []styles.Spec{{Colors: map[string]string{"primary": "1"}}},
			want:  []string{"no name"},
		},
		"a base nothing defines": {
			specs: []styles.Spec{{Name: "x", Base: "solarised"}},
			want:  []string{"solarised", "auto"},
		},
		"a colour role that is not one": {
			specs: []styles.Spec{{Name: "x", Colors: map[string]string{"primry": "1"}}},
			want:  []string{"primry", "primary"},
		},
		"a colour that will not parse": {
			specs: []styles.Spec{{Name: "x", Colors: map[string]string{"primary": "beige"}}},
			want:  []string{"beige", "primary"},
		},
		// Every problem is reported at once, so a file with two mistakes says so
		// once rather than over two runs.
		"two problems at once": {
			specs: []styles.Spec{{Name: "x", Colors: map[string]string{"primary": "beige", "nope": "1"}}},
			want:  []string{"beige", "nope"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := styles.Catalog(tc.specs)
			require.Error(t, err)
			for _, want := range tc.want {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestSelect(t *testing.T) {
	t.Parallel()

	themes := styles.BuiltinThemes()

	t.Run("by name", func(t *testing.T) {
		t.Parallel()

		at, err := styles.Select(themes, styles.ThemeLight)
		require.NoError(t, err)
		assert.Equal(t, 2, at)
	})

	t.Run("no name takes the first", func(t *testing.T) {
		t.Parallel()

		at, err := styles.Select(themes, "")
		require.NoError(t, err)
		assert.Equal(t, 0, at)
	})

	// A name that matches nothing is refused rather than silently falling back:
	// "my theme stopped working" has to have an answer.
	t.Run("a name nothing matches", func(t *testing.T) {
		t.Parallel()

		_, err := styles.Select(themes, "solarised")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "solarised")
		assert.Contains(t, err.Error(), `"dark"`)
	})

	t.Run("no themes at all", func(t *testing.T) {
		t.Parallel()

		_, err := styles.Select(nil, "")
		require.Error(t, err)
	})
}

// TestNewTheme pins that every style is a function of the theme's colours, so
// that switching theme is a matter of handing the panels a new set rather than
// rebuilding them.
func TestNewTheme(t *testing.T) {
	t.Parallel()

	theme := styles.Theme{Name: "test", Palette: styles.Palette{
		Primary:   lipgloss.Color("#111111"),
		Secondary: lipgloss.Color("#222222"),
		Muted:     lipgloss.Color("#333333"),
		Border:    lipgloss.Color("#444444"),
		Error:     lipgloss.Color("#555555"),
		Success:   lipgloss.Color("#666666"),
		Text:      lipgloss.Color("#777777"),
		Inverted:  lipgloss.Color("#888888"),
	}}

	st := styles.NewTheme(theme)
	assert.Equal(t, theme, st.Theme)
	assert.Equal(t, theme.Palette, st.Palette)
	assert.Equal(t, lipgloss.Color("#111111"), st.PanelTitle.GetForeground())
	assert.Equal(t, lipgloss.Color("#555555"), st.FieldError.GetForeground())
	assert.Equal(t, lipgloss.Color("#666666"), st.DiffAdded.GetForeground())
}

// TestNew_IsTheDefaultTheme pins that the no-argument constructor and the
// default theme have not drifted apart.
func TestNew_IsTheDefaultTheme(t *testing.T) {
	t.Parallel()

	assert.Equal(t, styles.DefaultTheme(), styles.New().Theme)
	assert.Equal(t, styles.DefaultPalette(), styles.New().Palette)
}

func TestRoleNames(t *testing.T) {
	t.Parallel()

	// The role names are what a config file writes, so they are pinned here:
	// renaming a field of Palette must not silently rename a config key.
	assert.Equal(t, []string{
		"primary", "secondary", "muted", "border", "error", "success", "text", "inverted",
	}, styles.RoleNames())
}
