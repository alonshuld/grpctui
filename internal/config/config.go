// Package config loads grpctui's configuration file.
//
// The file is the seam through which connection profiles (v0.4), environments
// (v0.7), and themes, keybindings, renderers and .proto files (v0.9) arrive, so
// it is a struct with a versioned shape rather than a bare string. Unknown keys
// are rejected:
// silently ignoring a typo'd setting is the worst possible behaviour for a
// config file.
//
// Secrets — bearer tokens, basic-auth passwords, API-key headers — belong in
// the environment rather than in the file, so any value may be written as
// ${VAR} and is replaced with that variable's value at load time. A missing
// variable is an error: a bearer token that quietly becomes the empty string
// produces an authentication failure that looks like anything but a config
// problem.
//
// That is not the same thing as the {{name}} references v0.7 added, and the two
// syntaxes are deliberately distinct. ${VAR} is the process environment,
// resolved once at load, and is how a secret reaches this file without being
// written in it; {{name}} is a grpctui variable, resolved when a request is
// sent, from a set the user can switch and add to while the TUI is running. A
// file that could not say which it meant would be a file that leaked one into
// the other. See [Environment] and internal/vars.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/alonshuld/grpctui/internal/format"
	"github.com/alonshuld/grpctui/internal/grpcclient"
)

// Config is grpctui's on-disk configuration.
//
// The top-level connection settings — target, tls, auth, metadata — describe
// one connection, which is all a user with a single server needs. Naming
// profiles is the step up from that, and the top-level settings become the
// first profile in the list when both are present.
type Config struct {
	// Version is the file format this config is written in. It may be left out —
	// and almost always is, since grpctui never writes this file — in which case
	// the file is read as the format this binary knows. Its whole job is to let a
	// config file written by a *later* grpctui say so, rather than failing on
	// whichever new key it happens to mention first. See internal/format.
	Version format.Version `yaml:"version"`

	// Target is the address to connect to when none is given on the command
	// line. A target argument always wins over this.
	Target string `yaml:"target"`

	// TLS, Auth and Metadata configure the connection to Target.
	TLS      TLS               `yaml:"tls"`
	Auth     Auth              `yaml:"auth"`
	Metadata map[string]string `yaml:"metadata"`

	// Profile names the profile to start on. Empty starts on the first one.
	Profile string `yaml:"profile"`

	// Profiles are the saved connections the switcher cycles through.
	Profiles []Profile `yaml:"profiles"`

	// Env names the environment to start in. Empty starts in the first one.
	Env string `yaml:"environment"`

	// Envs are the variable sets {{name}} references resolve against.
	Envs []Environment `yaml:"environments"`

	// ThemeName names the theme to draw in. Empty uses the built-in default,
	// which follows the terminal's own background.
	ThemeName string `yaml:"theme"`

	// Themes are the palettes the file defines, offered alongside the built-in
	// ones.
	Themes []Theme `yaml:"themes"`

	// Keys remaps keybindings, from an action name to the keys that trigger it.
	// An action nobody remaps keeps its built-in keys.
	Keys map[string]string `yaml:"keys"`

	// Proto names .proto sources to discover from instead of server reflection.
	// Empty — the ordinary case — leaves discovery asking the target.
	Proto Proto `yaml:"proto"`

	// Renderers switches response renderers off by name. Every renderer is on
	// unless it appears here set to false: one that had to be discovered and
	// enabled before it did anything is one nobody would ever see.
	Renderers map[string]bool `yaml:"renderers"`
}

// Proto is the .proto-file fallback for targets that do not serve reflection.
//
// It is top-level rather than per-profile because the files describe an API,
// not a connection: the same schema serves the dev, staging and production
// profiles of one service, and copying it into each would be three places to
// forget.
type Proto struct {
	// Files are the .proto files to compile, as paths on disk or relative to one
	// of ImportPaths. A leading ~ is expanded.
	Files []string `yaml:"files"`

	// ImportPaths are the directories an `import` statement is resolved against,
	// protoc's -I. A leading ~ is expanded.
	ImportPaths []string `yaml:"import_paths"`
}

// Paths returns the files and import paths with ${VAR} references and a leading
// ~ expanded, as every other path in this file is.
//
// Every path is expanded before anything is reported, so a file naming three
// unset variables says so once.
func (p Proto) Paths() (files, importPaths []string, err error) {
	var errs []error

	expandAll := func(in []string) []string {
		if len(in) == 0 {
			return nil
		}
		out := make([]string, 0, len(in))
		for _, s := range in {
			value, err := expand(s)
			if err != nil {
				errs = append(errs, err)
			}
			path, err := expandPath(value)
			if err != nil {
				errs = append(errs, err)
			}
			out = append(out, path)
		}
		return out
	}

	files = expandAll(p.Files)
	importPaths = expandAll(p.ImportPaths)
	return files, importPaths, errors.Join(errs...)
}

// Profile is one saved connection.
type Profile struct {
	// Name identifies the profile in the switcher and to --profile. It is
	// required: an unnamed profile cannot be switched to.
	Name string `yaml:"name"`

	Target string `yaml:"target"`

	TLS  TLS  `yaml:"tls"`
	Auth Auth `yaml:"auth"`

	// Metadata is the headers sent with every request on this connection. It is
	// a mapping rather than a list, which is nicer to write and costs the
	// ability to repeat a key; the metadata panel can still hold repeats at
	// runtime. Headers are sent in key order, so a file and a session agree on
	// what went out.
	Metadata map[string]string `yaml:"metadata"`
}

// TLS is the transport security half of a profile.
type TLS struct {
	// Enabled turns the connection into a TLS one. Every other field here is
	// inert without it, and setting one anyway is an error rather than a
	// surprise plaintext connection.
	Enabled bool `yaml:"enabled"`

	// CACert verifies the server against this PEM bundle instead of the host's
	// trust store. A leading ~ is expanded.
	CACert string `yaml:"ca_cert"`

	// ClientCert and ClientKey present a certificate to the server: mutual TLS.
	ClientCert string `yaml:"client_cert"`
	ClientKey  string `yaml:"client_key"`

	// ServerName overrides the name checked against the server's certificate.
	ServerName string `yaml:"server_name"`

	// InsecureSkipVerify accepts any certificate the server offers.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`
}

// Auth is the credential half of a profile.
type Auth struct {
	// Type is "bearer", "basic", or empty for no credential.
	Type string `yaml:"type"`

	Token    string `yaml:"token"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// DefaultPath reports the default config file path,
// $XDG_CONFIG_HOME/grpctui/config.yaml, falling back to
// ~/.config/grpctui/config.yaml.
//
// It returns an empty string when neither can be determined, which [Load]
// treats as "no config file" rather than as a startup failure — grpctui's whole
// pitch is that it needs no configuration.
func DefaultPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "grpctui", "config.yaml")
}

// Load reads the config file at path, which the caller has not been asked for
// by name — in practice [DefaultPath].
//
// A missing file yields the zero Config and no error: the default path is a
// suggestion, not a requirement. A file that exists but cannot be read or
// parsed is an error, because the user meant something by it.
func Load(path string) (Config, error) {
	return read(path, false)
}

// LoadFile reads a config file the user named on the command line. Every
// failure is an error, a missing file included: they asked for that path by
// name, and quietly starting up without it turns a typo into a mystery.
//
// The distinction is not cosmetic. "Missing is fine" applied to an explicit
// path is also what let `--config` accept a path that could not exist and carry
// on into the TUI — a shrug on one platform and an error on another, depending
// on whether the OS distinguishes ENOTDIR from ENOENT.
func LoadFile(path string) (Config, error) {
	return read(path, true)
}

func read(path string, mustExist bool) (Config, error) {
	if path == "" {
		return Config{}, nil
	}

	body, err := os.ReadFile(path) // #nosec G304 -- the path is the user's own config file, from --config or the XDG default.
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !mustExist {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("open config %q: %w", path, err)
	}

	// The version is read on its own pass, before the strict one, because the
	// strict pass is exactly what a file from a later grpctui would fail: it
	// would report the new key as unknown, which sends the reader hunting for a
	// typo instead of upgrading. See internal/format.
	if err := format.Check(body, fmt.Sprintf("config %q", path)); err != nil {
		return Config{}, err
	}

	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		// An empty file decodes to io.EOF, which means "nothing configured",
		// not "broken".
		if errors.Is(err, io.EOF) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}

	// Names are checked at load rather than at connect: a duplicate profile name
	// makes --profile ambiguous, and finding that out on the third connection of
	// the day is worse than finding it out at startup.
	if err := cfg.validateNames(); err != nil {
		return Config{}, fmt.Errorf("config %q: %w", path, err)
	}
	if err := cfg.validateEnvironmentNames(); err != nil {
		return Config{}, fmt.Errorf("config %q: %w", path, err)
	}
	if err := cfg.validateThemeNames(); err != nil {
		return Config{}, fmt.Errorf("config %q: %w", path, err)
	}
	return cfg, nil
}

// validateNames checks that every profile has a name and that no two share one.
func (c Config) validateNames() error {
	seen := make(map[string]bool, len(c.Profiles))
	for i, p := range c.Profiles {
		switch {
		case p.Name == "":
			return fmt.Errorf("profile %d has no name", i+1)
		case p.Name == defaultProfileName:
			return fmt.Errorf("profile %d: %q is the name of the top-level settings", i+1, defaultProfileName)
		case seen[p.Name]:
			return fmt.Errorf("profile %q is defined twice", p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}

// defaultProfileName is what the top-level connection settings are called once
// they sit in a list beside named profiles.
const defaultProfileName = "default"

// Connections converts the file into the connection profiles the UI switches
// between, in the order they were written. problems runs parallel to the
// profiles: problems[i] is why profiles[i] cannot be dialled, or nil.
//
// A profile that cannot be converted is listed rather than dropped, and its
// reason is carried instead of returned. One unset environment variable would
// otherwise make the whole file unusable — a production token nobody exported
// would stop you reaching your own laptop — where what it should stop is
// connecting to production.
//
// The top-level settings lead the list when they name a target, so a file that
// grew a `profiles` list keeps the connection it already had. A file with
// neither yields nothing, and the caller supplies the target from the command
// line.
func (c Config) Connections() (profiles []grpcclient.Profile, problems []error) {
	add := func(from Profile) {
		p, err := from.Connection()
		if err != nil && from.Name != "" {
			err = fmt.Errorf("profile %q: %w", from.Name, err)
		}
		profiles = append(profiles, p)
		problems = append(problems, err)
	}

	if c.Target != "" || c.TLS.Enabled || c.Auth.Type != "" || len(c.Metadata) > 0 {
		add(Profile{
			Name:     defaultProfileName,
			Target:   c.Target,
			TLS:      c.TLS,
			Auth:     c.Auth,
			Metadata: c.Metadata,
		})
	}
	for _, profile := range c.Profiles {
		add(profile)
	}
	return profiles, problems
}

// Connection converts one profile into its transport-layer form, expanding
// ${VAR} references and leading ~ as it goes.
//
// Every field is expanded before anything is reported, so a file missing three
// environment variables says so once rather than over three runs. The profile
// is returned either way, with whatever could not be expanded left as it was
// written: a connection nobody can dial is still one the switcher has to be
// able to name.
func (p Profile) Connection() (grpcclient.Profile, error) {
	var errs []error

	value := func(s string) string {
		v, err := expand(s)
		if err != nil {
			errs = append(errs, err)
		}
		return v
	}
	path := func(s string) string {
		v, err := expandPath(value(s))
		if err != nil {
			errs = append(errs, err)
		}
		return v
	}

	md, err := metadata(p.Metadata)
	if err != nil {
		errs = append(errs, err)
	}

	out := grpcclient.Profile{
		Name:   p.Name,
		Target: value(p.Target),
		Security: grpcclient.Security{
			TLS:                p.TLS.Enabled,
			CACert:             path(p.TLS.CACert),
			ClientCert:         path(p.TLS.ClientCert),
			ClientKey:          path(p.TLS.ClientKey),
			ServerName:         value(p.TLS.ServerName),
			InsecureSkipVerify: p.TLS.InsecureSkipVerify,
		},
		Auth: grpcclient.Auth{
			Kind:     grpcclient.AuthKind(p.Auth.Type),
			Token:    value(p.Auth.Token),
			Username: value(p.Auth.Username),
			Password: value(p.Auth.Password),
		},
		Metadata: md,
	}

	return out, errors.Join(errs...)
}

// metadata converts the mapping the file writes into the ordered headers the
// transport sends, sorted by key so that two runs of the same file send the
// same thing in the same order.
func metadata(m map[string]string) (grpcclient.Metadata, error) {
	if len(m) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	md := make(grpcclient.Metadata, 0, len(keys))
	for _, k := range keys {
		value, err := expand(m[k])
		if err != nil {
			return nil, fmt.Errorf("metadata %q: %w", k, err)
		}
		md = append(md, grpcclient.Header{Key: k, Value: value})
	}
	return md, nil
}

// Select finds the profile to start on by name, or the first one when no name
// was given.
//
// A name that matches nothing is an error listing what there was, because the
// alternative — connecting to whatever came first — is how a request meant for
// staging reaches production.
func Select(profiles []grpcclient.Profile, name string) (int, error) {
	if len(profiles) == 0 {
		return 0, errors.New("no connection profiles are configured")
	}
	if name == "" {
		return 0, nil
	}

	for i, p := range profiles {
		if p.Name == name {
			return i, nil
		}
	}

	names := make([]string, 0, len(profiles))
	for _, p := range profiles {
		names = append(names, strconv.Quote(p.Label()))
	}
	return 0, fmt.Errorf("no profile named %q: have %s", name, strings.Join(names, ", "))
}

// varPattern matches a ${VAR} reference.
//
// Only the braced form is a reference. A bare $ is left alone, so that a
// password of "p$ssw0rd" survives being written down — which is more valuable
// here than matching the shell exactly.
var varPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expand replaces every ${VAR} in s with that environment variable. A reference
// that cannot be resolved is left as written and reported: what comes back is
// something to show, never something to send.
func expand(s string) (string, error) {
	var missing []string

	out := varPattern.ReplaceAllStringFunc(s, func(match string) string {
		name := varPattern.FindStringSubmatch(match)[1]
		value, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
			return match
		}
		return value
	})

	if len(missing) > 0 {
		return out, fmt.Errorf("environment variable %s is not set", strings.Join(missing, ", "))
	}
	return out, nil
}

// expandPath resolves a leading ~, which a config file written by hand is very
// likely to contain and which the file APIs do not understand.
func expandPath(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand %q: %w", path, err)
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
}
