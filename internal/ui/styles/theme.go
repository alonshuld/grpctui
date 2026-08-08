package styles

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme is a named [Palette]. Everything grpctui draws is a style built from
// one, so swapping a theme is swapping eight colours rather than a hundred
// styles.
type Theme struct {
	Name string

	// Description is the one line the switcher shows beside the name.
	Description string

	Palette Palette
}

// The built-in theme names.
const (
	// ThemeAuto is the default: every colour is a light/dark pair and the
	// terminal's own background decides which half is used. It is right far more
	// often than a fixed choice, and wrong exactly when the terminal reports its
	// background incorrectly — which is what the other two are for.
	ThemeAuto = "auto"

	// ThemeDark and ThemeLight pin the half that [ThemeAuto] would have picked.
	// They exist because detection is a guess: a terminal behind tmux, ssh or a
	// CI log frequently answers wrongly, and "my status bar is invisible" needs
	// a fix that is one flag rather than a bug report.
	ThemeDark  = "dark"
	ThemeLight = "light"
)

// DefaultTheme is what grpctui uses when nothing says otherwise.
func DefaultTheme() Theme {
	return Theme{
		Name:        ThemeAuto,
		Description: "follow the terminal's background",
		Palette:     DefaultPalette(),
	}
}

// BuiltinThemes returns the themes that need no configuration, in the order the
// switcher walks them.
func BuiltinThemes() []Theme {
	return []Theme{
		DefaultTheme(),
		{
			Name:        ThemeDark,
			Description: "for a dark terminal",
			Palette:     fix(DefaultPalette(), dark),
		},
		{
			Name:        ThemeLight,
			Description: "for a light terminal",
			Palette:     fix(DefaultPalette(), light),
		},
	}
}

// side names which half of an [lipgloss.AdaptiveColor] pair to keep.
type side int

const (
	light side = iota
	dark
)

// fix collapses every adaptive colour in p onto one side, which is what turns
// the auto theme into the light and dark ones. A colour that was never adaptive
// is left as it is.
func fix(p Palette, s side) Palette {
	pick := func(c lipgloss.TerminalColor) lipgloss.TerminalColor {
		a, ok := c.(lipgloss.AdaptiveColor)
		if !ok {
			return c
		}
		if s == dark {
			return lipgloss.Color(a.Dark)
		}
		return lipgloss.Color(a.Light)
	}

	out := p
	for _, role := range roles {
		role.set(&out, pick(role.get(p)))
	}
	return out
}

// Spec is a theme as a config file writes it: a name, the built-in theme its
// unmentioned colours come from, and the roles that differ.
//
// It is plain strings rather than [Palette] so that internal/config can carry
// one without importing a UI package — the same reason internal/requests holds
// a body as `any`. Turning the strings into colours is this package's job,
// because what a colour may look like is this package's knowledge.
type Spec struct {
	Name string

	// Base names the theme the spec starts from, or is empty for [ThemeAuto].
	// Overriding two colours of the dark theme should not mean writing the other
	// six out.
	Base string

	// Colors maps a role name — "primary", "border", "error" — to a colour. A
	// name that is not a role is an error: a theme where `primry` is silently
	// ignored is a theme the user cannot debug.
	Colors map[string]string
}

// role is one entry of [Palette], named so that a config file can address it.
type role struct {
	name string
	get  func(Palette) lipgloss.TerminalColor
	set  func(*Palette, lipgloss.TerminalColor)
}

// roles is the addressable surface of a [Palette], in the order a theme listing
// prints them. It is written out rather than reflected over so that the names a
// config file may use are a fact of this file and not of the struct's field
// names.
var roles = []role{
	{
		"primary", func(p Palette) lipgloss.TerminalColor { return p.Primary },
		func(p *Palette, c lipgloss.TerminalColor) { p.Primary = c },
	},
	{
		"secondary", func(p Palette) lipgloss.TerminalColor { return p.Secondary },
		func(p *Palette, c lipgloss.TerminalColor) { p.Secondary = c },
	},
	{
		"muted", func(p Palette) lipgloss.TerminalColor { return p.Muted },
		func(p *Palette, c lipgloss.TerminalColor) { p.Muted = c },
	},
	{
		"border", func(p Palette) lipgloss.TerminalColor { return p.Border },
		func(p *Palette, c lipgloss.TerminalColor) { p.Border = c },
	},
	{
		"error", func(p Palette) lipgloss.TerminalColor { return p.Error },
		func(p *Palette, c lipgloss.TerminalColor) { p.Error = c },
	},
	{
		"success", func(p Palette) lipgloss.TerminalColor { return p.Success },
		func(p *Palette, c lipgloss.TerminalColor) { p.Success = c },
	},
	{
		"text", func(p Palette) lipgloss.TerminalColor { return p.Text },
		func(p *Palette, c lipgloss.TerminalColor) { p.Text = c },
	},
	{
		"inverted", func(p Palette) lipgloss.TerminalColor { return p.Inverted },
		func(p *Palette, c lipgloss.TerminalColor) { p.Inverted = c },
	},
}

// RoleNames lists the colour roles a theme may set, for an error message that
// says what was allowed instead of only what was wrong.
func RoleNames() []string {
	names := make([]string, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.name)
	}
	return names
}

// Catalog assembles the themes available this session: the built-in ones
// followed by whatever the config file defines.
//
// A custom theme may take a built-in one's name, in which case it replaces it
// in place. That is deliberate: somebody who dislikes one colour of the dark
// theme should be able to say so without their theme also appearing twice in
// the switcher under a new name.
//
// Every spec is converted before anything is reported, so a file with three bad
// colours in it says so once rather than over three runs.
func Catalog(custom []Spec) ([]Theme, error) {
	out := BuiltinThemes()

	var errs []error
	for _, spec := range custom {
		theme, err := spec.Theme(out)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if at := indexOf(out, theme.Name); at >= 0 {
			out[at] = theme
			continue
		}
		out = append(out, theme)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

// Theme converts a spec against the themes already known, which is what lets
// Base name a built-in one — or an earlier custom one, so that a file can
// define a palette once and vary it twice.
func (s Spec) Theme(known []Theme) (Theme, error) {
	if s.Name == "" {
		return Theme{}, errors.New("theme has no name")
	}

	base := DefaultTheme()
	if s.Base != "" {
		at := indexOf(known, s.Base)
		if at < 0 {
			return Theme{}, fmt.Errorf("theme %q: no theme named %q to start from: have %s",
				s.Name, s.Base, quotedNames(known))
		}
		base = known[at]
	}

	out := Theme{Name: s.Name, Description: s.description(base), Palette: base.Palette}

	// Sorted, so that a spec with two bad roles in it reports them the same way
	// twice: Go's map iteration order is deliberately not stable.
	names := make([]string, 0, len(s.Colors))
	for name := range s.Colors {
		names = append(names, name)
	}
	slices.Sort(names)

	var errs []error
	for _, name := range names {
		at := slices.IndexFunc(roles, func(r role) bool { return r.name == name })
		if at < 0 {
			errs = append(errs, fmt.Errorf("theme %q: %q is not a colour: have %s",
				s.Name, name, strings.Join(RoleNames(), ", ")))
			continue
		}
		colour, err := ParseColor(s.Colors[name])
		if err != nil {
			errs = append(errs, fmt.Errorf("theme %q: %s: %w", s.Name, name, err))
			continue
		}
		roles[at].set(&out.Palette, colour)
	}
	if err := errors.Join(errs...); err != nil {
		return Theme{}, err
	}
	return out, nil
}

// description says where a custom theme came from, since it has no prose of its
// own and a switcher row with an empty second column looks broken.
func (s Spec) description(base Theme) string {
	if s.Base == "" {
		return "from the config file"
	}
	return "from the config file, over " + base.Name
}

// Select finds the theme to start with by name, or the first one when no name
// was given.
//
// It mirrors config.Select for profiles, including the error: a name that
// matches nothing lists what there was, because a silent fallback to the
// default is how "my theme stopped working" becomes unanswerable.
func Select(themes []Theme, name string) (int, error) {
	if len(themes) == 0 {
		return 0, errors.New("no themes are configured")
	}
	if name == "" {
		return 0, nil
	}
	if at := indexOf(themes, name); at >= 0 {
		return at, nil
	}
	return 0, fmt.Errorf("no theme named %q: have %s", name, quotedNames(themes))
}

func indexOf(themes []Theme, name string) int {
	return slices.IndexFunc(themes, func(t Theme) bool { return t.Name == name })
}

func quotedNames(themes []Theme) string {
	names := make([]string, 0, len(themes))
	for _, t := range themes {
		names = append(names, strconv.Quote(t.Name))
	}
	return strings.Join(names, ", ")
}

// hexPattern matches a #rgb or #rrggbb colour.
var hexPattern = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// ParseColor reads a colour as a config file writes it.
//
// Three forms are accepted: a hex colour (`#7D56F4`, `#b9f`), an ANSI palette
// index (`0` to `255`), and a `light/dark` pair, which yields a colour that
// follows the terminal's background exactly as the built-in themes do.
//
// An unrecognised string is an error rather than a colour lipgloss quietly
// renders as the default foreground — a theme that half worked would be worse
// than one that refused to load.
func ParseColor(s string) (lipgloss.TerminalColor, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("no colour given")
	}

	if lightHalf, darkHalf, ok := strings.Cut(s, "/"); ok {
		l, err := parseSolid(lightHalf)
		if err != nil {
			return nil, err
		}
		d, err := parseSolid(darkHalf)
		if err != nil {
			return nil, err
		}
		return lipgloss.AdaptiveColor{Light: l, Dark: d}, nil
	}

	c, err := parseSolid(s)
	if err != nil {
		return nil, err
	}
	return lipgloss.Color(c), nil
}

// parseSolid validates one colour and returns it as lipgloss spells it, which
// for both accepted forms is the string itself.
func parseSolid(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", errors.New("no colour given")
	case hexPattern.MatchString(s):
		return s, nil
	}

	if n, err := strconv.Atoi(s); err == nil && n >= 0 && n <= 255 {
		return s, nil
	}
	return "", fmt.Errorf("%q is not a colour: want #rrggbb, an ANSI number 0-255, or `light/dark`", s)
}
