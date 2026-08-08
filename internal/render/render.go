// Package render turns values inside a response into something a human can
// read at a glance, and is the extension point for doing so.
//
// # What it does, and what it deliberately does not
//
// A renderer *annotates*; it never rewrites. `"2026-08-08T09:14:02Z"` stays
// exactly what the server sent, and "3 minutes ago" appears beside it. That is
// not a stylistic choice: grpctui's whole pitch is reading the wire rather than
// a prettified account of it, and a response panel that silently replaced a
// value with an interpretation of it would be lying about the one thing the
// tool exists to show. It would also be unusable for the case that matters
// most — the server whose timestamps are wrong.
//
// # The extension point
//
// A [Renderer] claims one or more fully-qualified message type names and turns
// a message of that type into a line of text. Registering one is all it takes
// for every field of that type, at any depth of any response, to be annotated:
// [Registry.Annotate] finds them by descriptor, so nothing has to be taught
// where the field is. The built-ins are ordinary renderers registered the same
// way — see builtin.go — and exist as much to show the shape as to be useful.
//
// It is deliberately not a plugin *process*. Loading foreign code into a
// terminal client that holds bearer tokens is a bad trade for the convenience,
// and Go has no stable plugin story on the platforms grpctui ships to. What
// this offers is the seam: a renderer is an interface, the registry is a value,
// and a fork or a future build tag adds one in a few lines.
package render

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Renderer turns a message of the types it claims into a line of display text.
type Renderer interface {
	// Name identifies the renderer to a config file, so that it can be switched
	// off. It is lowercase and hyphenated, like every other name a user types.
	Name() string

	// Types are the fully-qualified message names this renderer claims,
	// e.g. "google.protobuf.Timestamp".
	Types() []string

	// Render returns the text to show beside the message, or false to leave it
	// alone. Returning false is the right answer for a value the renderer cannot
	// make sense of: a gloss that says something wrong is worse than no gloss.
	//
	// now is the clock to read for anything relative. It is passed rather than
	// taken from time.Now so that a frame is a function of the messages that
	// produced it — the same reason internal/ui takes one.
	Render(msg protoreflect.Message, now time.Time) (string, bool)
}

// Annotation is one gloss, and the line of the rendered body it belongs beside.
type Annotation struct {
	// Line is 1-based, counting lines of the body handed to [Registry.Annotate].
	Line int

	// Text is what to show, without decoration: the caller styles it.
	Text string
}

// Registry is the set of renderers in force. The zero Registry annotates
// nothing, which is exactly what a response panel with no renderers should do.
//
// It is a value rather than a package-level singleton, for the reason
// internal/logging refuses a global logger: a registry that a test could
// mutate out from under another test is a registry that makes failures
// non-reproducible.
type Registry struct {
	byType map[protoreflect.FullName]Renderer
	names  []string
}

// New builds a registry from renderers, later ones losing to earlier ones on a
// type they both claim.
//
// A duplicate is an error rather than a silent precedence rule: two renderers
// competing for google.protobuf.Timestamp is a configuration mistake, and which
// one wins is not something a user should have to discover by looking at the
// output.
func New(renderers ...Renderer) (Registry, error) {
	r := Registry{byType: make(map[protoreflect.FullName]Renderer, len(renderers))}

	var errs []error
	for _, renderer := range renderers {
		r.names = append(r.names, renderer.Name())
		for _, name := range renderer.Types() {
			typ := protoreflect.FullName(name)
			if existing, ok := r.byType[typ]; ok {
				errs = append(errs, fmt.Errorf("renderers %q and %q both render %s",
					existing.Name(), renderer.Name(), name))
				continue
			}
			r.byType[typ] = renderer
		}
	}
	if err := errors.Join(errs...); err != nil {
		return Registry{}, err
	}
	return r, nil
}

// Names lists the registered renderers, in the order they were given.
func (r Registry) Names() []string { return slices.Clone(r.names) }

// Empty reports whether the registry would annotate nothing whatever it was
// given, which lets a caller skip the walk entirely.
func (r Registry) Empty() bool { return len(r.byType) == 0 }

// Annotate finds the glosses for a message and says which line of body each
// belongs beside.
//
// body must be the JSON rendering of msg — what internal/protoschema produced
// for the response panel. The two are walked separately and joined by field
// path, rather than the JSON being parsed back into a message, because the
// message is where the *types* are: JSON has forgotten that a string was a
// Timestamp, which is the one thing a renderer needs to know.
//
// A body that is not JSON — protobuf's text format, which [protoschema.Marshal]
// falls back to — yields no annotations rather than an error. There is nothing
// wrong in that case; there is simply nothing to attach a gloss to.
func (r Registry) Annotate(msg proto.Message, body string, now time.Time) []Annotation {
	if r.Empty() || msg == nil || body == "" {
		return nil
	}

	glosses := make(map[string]string)
	r.walk(msg.ProtoReflect(), "", now, glosses)
	if len(glosses) == 0 {
		return nil
	}

	lines := jsonLines(body)
	out := make([]Annotation, 0, len(glosses))
	for path, text := range glosses {
		if line, ok := lines[path]; ok {
			out = append(out, Annotation{Line: line, Text: text})
		}
	}

	// By line, so that a caller can walk annotations and body together, and so
	// that the result does not depend on Go's map iteration order.
	slices.SortFunc(out, func(a, b Annotation) int { return a.Line - b.Line })
	return out
}

// walk collects a gloss for every populated field whose type a renderer claims,
// keyed by the path the same value has in the JSON rendering.
//
// Only populated fields are visited, which is what [protoreflect.Message.Range]
// does anyway and is also correct: an unset message field is `null` in the JSON
// and there is nothing there to describe.
func (r Registry) walk(m protoreflect.Message, prefix string, now time.Time, out map[string]string) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		path := join(prefix, fd.JSONName())

		switch {
		case fd.IsMap():
			// protojson writes a map as an object keyed by the map key, so the
			// path of an entry is the key itself rather than an index.
			v.Map().Range(func(k protoreflect.MapKey, value protoreflect.Value) bool {
				r.value(fd.MapValue(), value, join(path, k.String()), now, out)
				return true
			})
		case fd.IsList():
			list := v.List()
			for i := range list.Len() {
				r.value(fd, list.Get(i), fmt.Sprintf("%s[%d]", path, i), now, out)
			}
		default:
			r.value(fd, v, path, now, out)
		}
		return true
	})
}

// value handles one value: a message either has a renderer or is descended
// into, and anything else is left alone.
//
// A renderer takes precedence over descending. A type somebody has claimed is a
// type they have said how to show, and glossing its innards as well would be
// annotating the same value twice.
func (r Registry) value(fd protoreflect.FieldDescriptor, v protoreflect.Value, path string, now time.Time, out map[string]string) {
	if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
		return
	}
	msg := v.Message()

	if renderer, ok := r.byType[msg.Descriptor().FullName()]; ok {
		if text, ok := renderer.Render(msg, now); ok {
			out[path] = text
		}
		return
	}
	r.walk(msg, path, now, out)
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// jsonLines maps each value's path in a rendered JSON body to the 1-based line
// it starts on.
//
// It walks tokens rather than the lines themselves because a path is a fact
// about the structure and not about the indentation: the body is indented by
// [protoschema.MarshalJSON] and looks predictable today, but a walk that
// depended on that would break the day anything about the layout changed. A
// body that does not parse yields nothing, which is the right answer for the
// text-format fallback.
func jsonLines(body string) map[string]int {
	w := walker{
		body: body,
		dec:  json.NewDecoder(strings.NewReader(body)),
		out:  make(map[string]int),
	}

	for {
		token, err := w.dec.Token()
		if err != nil {
			// io.EOF is the end; anything else is a body that stopped parsing
			// halfway, and what was found before that point is kept. There is
			// nothing to gain by discarding it — those paths are still where they
			// said they were.
			return w.out
		}
		w.token(token)
	}
}

// walker is the state a token walk carries: where it is in the structure, and
// what it has found.
type walker struct {
	body string
	dec  *json.Decoder
	out  map[string]int

	// path is the way to whatever comes next: object keys (each carrying its own
	// leading ".") and list indices, innermost last. inObject runs parallel to
	// it, saying whether each level is an object — so the next token names a
	// field — or a list, where it occupies a position.
	path     []string
	inObject []bool

	// pendingKey says the next token is an object's key rather than a value.
	pendingKey bool
}

// token advances the walk by one token.
func (w *walker) token(token json.Token) {
	if w.pendingKey {
		if key, ok := token.(string); ok {
			w.path[len(w.path)-1] = "." + key
			w.pendingKey = false
			return
		}
	}

	delim, isDelim := token.(json.Delim)
	switch {
	case isDelim && (delim == '{' || delim == '['):
		w.open(delim)
		return
	case isDelim:
		// The closing delimiter pops the slot this container's children were
		// written into, and then the container itself.
		w.path = w.path[:len(w.path)-1]
		w.inObject = w.inObject[:len(w.inObject)-1]
	default:
		w.record()
	}
	w.advance()
}

// open descends into a container, recording where it starts.
func (w *walker) open(delim json.Delim) {
	w.record()
	w.inObject = append(w.inObject, delim == '{')

	// An object's first child is named by the key that follows; a list's is at
	// index zero and names itself.
	if delim == '{' {
		w.path = append(w.path, "")
		w.pendingKey = true
		return
	}
	w.path = append(w.path, "[0]")
	w.pendingKey = false
}

// advance moves the enclosing container on: an object wants another key, a list
// the next index.
func (w *walker) advance() {
	if len(w.inObject) == 0 {
		return
	}
	if w.inObject[len(w.inObject)-1] {
		w.pendingKey = true
		return
	}
	w.path[len(w.path)-1] = nextIndex(w.path[len(w.path)-1])
}

// record notes the line the value just read starts on.
//
// The decoder's offset sits just past that token, and no JSON value spans a
// line — a string's newlines are escaped — so counting the newlines before it
// gives the line the value is on.
func (w *walker) record() {
	here := strings.Join(w.path, "")
	if here == "" {
		return
	}

	at := min(int(w.dec.InputOffset()), len(w.body))
	w.out[trimLeadingDot(here)] = strings.Count(w.body[:at], "\n") + 1
}

// nextIndex advances a list slot, which is written the way a path writes one.
func nextIndex(slot string) string {
	digits := strings.TrimSuffix(strings.TrimPrefix(slot, "["), "]")
	i, err := strconv.Atoi(digits)
	if err != nil {
		return "[0]"
	}
	return "[" + strconv.Itoa(i+1) + "]"
}

// trimLeadingDot removes the separator a nested key contributes, since paths
// join with "." between names but nothing before the first one.
func trimLeadingDot(path string) string { return strings.TrimPrefix(path, ".") }
