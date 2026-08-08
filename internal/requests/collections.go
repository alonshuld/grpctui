package requests

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alonshuld/grpctui/internal/format"
)

// DefaultCollection is where a request saved without a collection name goes.
const DefaultCollection = "default"

// ErrNoCollectionsDir is returned by [Collections.Save] when there is nowhere
// to write. It is a distinct error because the UI reports it differently from a
// failed write: nothing went wrong, the feature is simply switched off.
var ErrNoCollectionsDir = errors.New("no collections directory is configured")

// Collection is one named file of saved requests.
//
// A collection is meant to live beside a project and be committed: named
// requests, in the order the user put them, with bodies written as ordinary
// YAML. Nothing in a collection is generated — grpctui only ever adds, replaces
// or removes a whole entry, so a hand-written comment on the line above one
// survives being edited from the TUI.
type Collection struct {
	// Name is the file's basename without its extension, and how the collection
	// is referred to everywhere else.
	Name string `yaml:"-"`

	// Path is the file the collection was read from or will be written to.
	Path string `yaml:"-"`

	// Version is the file format. It is written on every save and may be left
	// out of a hand-written file, which is then read as this binary's own
	// format. A collection is the file most likely to be shared between two
	// machines running different grpctuis — it is committed beside a project and
	// replayed in CI — so it is the one that most needs to be able to say which
	// format it is in. See internal/format.
	Version format.Version `yaml:"version"`

	Requests []Request `yaml:"requests"`
}

// Collections is every collection found in one directory, ordered by name.
type Collections struct {
	// Dir is the directory holding the files. An empty Dir makes collections
	// read-only-and-empty: the browser still works, saving reports
	// [ErrNoCollectionsDir].
	Dir string

	list []Collection
}

// DefaultCollectionsDir reports the default collections directory,
// $XDG_CONFIG_HOME/grpctui/collections, falling back to
// ~/.config/grpctui/collections.
//
// Collections sit beside config.yaml rather than in the state directory because
// they are the same kind of thing: written by a person, meant to be kept.
func DefaultCollectionsDir() string {
	dir := configDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "collections")
}

// LoadCollections reads every .yaml and .yml file in dir.
//
// A missing directory yields no collections and no error — most users will
// never create one. A file that cannot be parsed is an error naming it, because
// a collection is hand-edited and a silently skipped file looks exactly like a
// request that was never saved.
func LoadCollections(dir string) (Collections, error) {
	c := Collections{Dir: dir}
	if dir == "" {
		return c, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return c, nil
		}
		return Collections{}, fmt.Errorf("read %q: %w", dir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !isCollectionFile(entry.Name()) {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		col := Collection{Name: collectionName(entry.Name()), Path: path}
		if err := readYAML(path, &col); err != nil {
			return Collections{}, err
		}
		c.list = append(c.list, col)
	}

	// os.ReadDir already sorts by filename, and a collection's name is its
	// filename minus the extension — except that "a.yml" and "a.yaml" would
	// collide. Sorting by name keeps the order stable whichever extension won.
	slices.SortStableFunc(c.list, func(a, b Collection) int {
		return strings.Compare(a.Name, b.Name)
	})
	return c, nil
}

// All returns the collections, ordered by name.
func (c Collections) All() []Collection { return c.list }

// Len reports how many collections there are.
func (c Collections) Len() int { return len(c.list) }

// Names lists the collection names, for a hint offering somewhere to save to.
func (c Collections) Names() []string {
	names := make([]string, 0, len(c.list))
	for _, col := range c.list {
		names = append(names, col.Name)
	}
	return names
}

// Save puts a request into a collection and writes that collection's file.
//
// An entry whose name is already taken is replaced in place rather than
// appended: saving over a request you have just edited is the ordinary case,
// and two entries with one name would make the second unreachable. Saving under
// a new name is therefore how you duplicate one.
//
// Only the one file is rewritten. The others are not grpctui's to touch, and a
// directory full of a team's collections should not be reformatted because
// somebody saved a request.
func (c *Collections) Save(collection string, r Request) error {
	if err := ValidateName(collection); err != nil {
		return fmt.Errorf("collection %q: %w", collection, err)
	}
	if err := ValidateName(r.Name); err != nil {
		return fmt.Errorf("request %q: %w", r.Name, err)
	}
	if c.Dir == "" {
		return ErrNoCollectionsDir
	}

	col := Collection{Name: collection, Path: filepath.Join(c.Dir, collection+".yaml")}
	i := slices.IndexFunc(c.list, func(e Collection) bool { return e.Name == collection })
	if i >= 0 {
		col = c.list[i]
	}

	// The entry list is cloned rather than appended to in place: Collections is
	// held by value in a bubbletea model, and a copy taken before a save must not
	// see the save land in a slice it shares.
	col.Requests = slices.Clone(col.Requests)
	if j := slices.IndexFunc(col.Requests, func(e Request) bool { return e.Name == r.Name }); j >= 0 {
		col.Requests[j] = r
	} else {
		col.Requests = append(col.Requests, r)
	}

	// Saving stamps the current format on the file, whatever it said before. The
	// entries about to be written are this binary's shape, so claiming an older
	// version would be a lie that a later reader would act on.
	col.Version = format.Current

	if err := writeYAML(col.Path, col); err != nil {
		return err
	}

	if i >= 0 {
		c.list = slices.Clone(c.list)
		c.list[i] = col
		return nil
	}
	c.list = append(slices.Clone(c.list), col)
	slices.SortStableFunc(c.list, func(a, b Collection) int { return strings.Compare(a.Name, b.Name) })
	return nil
}

// ValidateName checks a collection or request name.
//
// A collection name becomes a filename, so anything that could escape the
// directory or name a file the user did not mean is refused outright rather
// than sanitised: "prod/../../.ssh/config" silently becoming "config" is worse
// than being told no. Request names are held to the same rule for consistency,
// and because a name with a newline in it renders as two rows.
func ValidateName(name string) error {
	switch {
	case name == "":
		return errors.New("a name is required")
	case strings.ContainsAny(name, `/\`):
		// Both separators, on every platform: a collection written on Linux is
		// read on Windows, where a backslash would suddenly be a directory.
		return errors.New("a name may not contain a path")
	case name == "." || name == "..":
		return errors.New(`"." and ".." are not names`)
	}

	for _, r := range name {
		if r < ' ' || r == 0x7f {
			return errors.New("a name may not contain control characters")
		}
	}
	return nil
}

// SplitName reads the "collection/request" form the save prompt accepts. A
// bare name goes to [DefaultCollection], which is what a user with one
// collection never has to think about.
func SplitName(input string) (collection, name string) {
	input = strings.TrimSpace(input)
	if before, after, ok := strings.Cut(input, "/"); ok {
		return strings.TrimSpace(before), strings.TrimSpace(after)
	}
	return DefaultCollection, input
}

func isCollectionFile(name string) bool {
	ext := filepath.Ext(name)
	return ext == ".yaml" || ext == ".yml"
}

func collectionName(file string) string {
	return strings.TrimSuffix(file, filepath.Ext(file))
}
