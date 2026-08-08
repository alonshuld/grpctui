package main

import (
	"github.com/alonshuld/grpctui/internal/config"
	"github.com/alonshuld/grpctui/internal/render"
	"github.com/alonshuld/grpctui/internal/ui/keys"
	"github.com/alonshuld/grpctui/internal/ui/styles"
)

// appearance is everything the flags and the config file decide about the way
// grpctui presents itself: the palettes and the keyboard.
//
// They travel together because they are the same kind of decision — made once
// at startup, applied to every panel, and belonging to the user rather than to
// the connection — and because threading two more parameters through [start]
// would say less than one named thing does.
type appearance struct {
	palettes  palettes
	keys      keys.KeyMap
	renderers render.Registry
}

// look assembles the themes, the keymap and the response renderers.
func look(cfg config.Config, opts options) (appearance, error) {
	p, err := themes(cfg, opts)
	if err != nil {
		return appearance{}, err
	}

	km, err := keys.Default().Apply(cfg.Keys)
	if err != nil {
		return appearance{}, err
	}

	enabled, err := render.Enabled(cfg.Renderers)
	if err != nil {
		return appearance{}, err
	}
	registry, err := render.New(enabled...)
	if err != nil {
		return appearance{}, err
	}

	return appearance{palettes: p, keys: km, renderers: registry}, nil
}

// palettes is everything the flags and the config file decide about how
// grpctui looks: the themes available, and which one to start in.
type palettes struct {
	list   []styles.Theme
	active int
}

// themes assembles the palettes and says which one to draw in.
//
// Unlike profiles and environments there is no parallel list of problems here,
// and a bad theme is fatal rather than set aside. The reason those two defer a
// failure is that one unusable production profile must not stop you reaching
// your laptop; a theme has no such blast radius, and a colour that will not
// parse is a typo the user can fix in the file they just edited.
func themes(cfg config.Config, opts options) (palettes, error) {
	list, err := styles.Catalog(specs(cfg.Themes))
	if err != nil {
		return palettes{}, err
	}

	// The flag wins over the file, as it does everywhere else: --theme dark is
	// how somebody whose terminal reports its background wrongly gets through
	// one session without editing anything.
	name := cfg.ThemeName
	if opts.theme != "" {
		name = opts.theme
	}

	active, err := styles.Select(list, name)
	if err != nil {
		return palettes{}, err
	}
	return palettes{list: list, active: active}, nil
}

// specs converts the file's themes into the form internal/ui/styles reads.
//
// The conversion happens here rather than in internal/config because styles is
// a UI package and config is below it: config maps YAML onto plain strings, and
// main is where the layers meet. It is the same shape as the profile and
// environment conversions, run the other way up.
func specs(themes []config.Theme) []styles.Spec {
	if len(themes) == 0 {
		return nil
	}

	out := make([]styles.Spec, 0, len(themes))
	for _, t := range themes {
		out = append(out, styles.Spec{Name: t.Name, Base: t.Base, Colors: t.Colors})
	}
	return out
}

// theme is the palette to start in.
func (p palettes) theme() styles.Theme {
	if p.active < 0 || p.active >= len(p.list) {
		return styles.DefaultTheme()
	}
	return p.list[p.active]
}
