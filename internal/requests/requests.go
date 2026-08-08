// Package requests stores the calls a user has made and the ones they have
// chosen to keep.
//
// Two things live here, and they are deliberately different. [History] is
// machine-managed state: every send is appended to it, the oldest fall off the
// end, and nobody is expected to open the file. A [Collection] is the opposite —
// a file the user names, keeps beside a project, hand-edits and commits, which
// is why both are YAML with the request body written out as ordinary nested
// keys rather than as an escaped JSON blob.
//
// # What is never written down
//
// A record carries header *names* and nothing else. grpctui's standing rule is
// that a bearer token, a basic-auth password and a header value never reach a
// surface that outlives the call, and a history file is exactly such a surface:
// it sits in the state directory for weeks, and a collection is meant to be
// committed. Restoring a request therefore restores its shape and leaves the
// values to the connection — see [Request.Headers].
//
// Nothing here imports protobuf. A body is the generic structure protobuf's
// JSON mapping decodes to, and internal/protoschema owns the conversion in both
// directions; that is what keeps this package a storage layer rather than a
// second schema layer.
package requests

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Request is one call: the method, the filled-in body, the header names that
// went with it, and when.
//
// The same type serves history and collections, because they hold the same
// thing for different reasons. Name is what a collection entry is called and is
// empty in history, where the timestamp identifies an entry instead.
type Request struct {
	// Name identifies a saved request within its collection. It is required
	// there — an unnamed entry cannot be looked up or replaced — and unused in
	// history.
	Name string `yaml:"name,omitempty"`

	// Method is the fully-qualified method name, e.g. helloworld.Greeter.SayHello.
	Method string `yaml:"method"`

	// Kind is the method's streaming shape, recorded for display only: what a
	// replay actually does is decided by the live descriptor, not by this.
	Kind string `yaml:"kind,omitempty"`

	// Target and Profile say where the request went. They are context for the
	// user reading a list, not instructions — replaying a request sends it to
	// whatever connection is open now.
	Target  string `yaml:"target,omitempty"`
	Profile string `yaml:"profile,omitempty"`

	// Headers are the names of the metadata that went out, without their values.
	// See the package comment: a value is a credential often enough that
	// recording one is never worth it.
	Headers []string `yaml:"headers,omitempty"`

	// Body is the request message under protobuf's JSON mapping, decoded into
	// maps, slices, strings, numbers and bools. Writing it structurally is what
	// makes a collection diffable; internal/protoschema converts it to and from
	// a message.
	Body any `yaml:"body,omitempty"`

	// Values are the {{variable}} references the body cannot hold, keyed by
	// field path — "user.id", "tags[1]".
	//
	// A request is recorded as it was typed, references and all: that is what
	// makes a collection portable between environments, and it is the only way
	// to keep grpctui's rule about credentials true here, since a token captured
	// out of a login response is exactly what a chained request refers to. Most
	// references need no entry — one in a string field is a perfectly good
	// string and rides along in Body — so this holds only the ones protobuf's
	// JSON mapping would reject, and those fields sit at their zero value in
	// Body until [protoschema.Form.LoadValues] puts the text back.
	Values map[string]string `yaml:"values,omitempty"`

	// SentAt is when the call was made, in UTC. A saved request keeps the
	// timestamp of the call it was saved from.
	SentAt time.Time `yaml:"sent_at,omitempty"`
}

// Label is what to call the request in a list: its name if it has one, and
// otherwise the bare method name, which is what a history entry has instead.
func (r Request) Label() string {
	if r.Name != "" {
		return r.Name
	}
	return r.ShortMethod()
}

// ShortMethod is the method without its package and service, for a column too
// narrow to spend thirty cells on a prefix every row shares.
func (r Request) ShortMethod() string {
	if i := strings.LastIndex(r.Method, "."); i >= 0 {
		return r.Method[i+1:]
	}
	return r.Method
}

// Service is the fully-qualified service the method belongs to.
func (r Request) Service() string {
	if i := strings.LastIndex(r.Method, "."); i >= 0 {
		return r.Method[:i]
	}
	return ""
}

// Search is the lowercased text a filter query is matched against: the name,
// the method, and the body's keys and scalar values.
//
// Field *content* is searchable because that is how you find a call again — you
// remember the account id you passed, not that it was the fourth SayHello of
// the afternoon. It is built once and cached by the caller rather than on every
// keystroke; see [Matcher].
func (r Request) Search() string {
	var b strings.Builder
	b.WriteString(strings.ToLower(r.Name))
	b.WriteByte('\n')
	b.WriteString(strings.ToLower(r.Method))
	b.WriteByte('\n')
	flatten(&b, r.Body)

	// A field filled in from a variable is not in the body, so without this the
	// one thing a user remembers about the call — that it was the one using
	// {{tenant}} — would be the one thing they could not search for.
	for path, value := range r.Values {
		b.WriteString(strings.ToLower(path))
		b.WriteByte('\n')
		b.WriteString(strings.ToLower(value))
		b.WriteByte('\n')
	}
	return b.String()
}

// flatten appends every key and scalar in a decoded body to b, one per line.
func flatten(b *strings.Builder, body any) {
	switch v := body.(type) {
	case map[string]any:
		// Keys are written in whatever order the map yields them. The result is
		// only ever substring-matched, so the order does not matter — and sorting
		// it would cost an allocation per node on a structure that can be deep.
		for key, value := range v {
			b.WriteString(strings.ToLower(key))
			b.WriteByte('\n')
			flatten(b, value)
		}
	case []any:
		for _, item := range v {
			flatten(b, item)
		}
	case string:
		b.WriteString(strings.ToLower(v))
		b.WriteByte('\n')
	case bool:
		b.WriteString(strconv.FormatBool(v))
		b.WriteByte('\n')
	case int:
		b.WriteString(strconv.Itoa(v))
		b.WriteByte('\n')
	case int64:
		b.WriteString(strconv.FormatInt(v, 10))
		b.WriteByte('\n')
	case uint64:
		b.WriteString(strconv.FormatUint(v, 10))
		b.WriteByte('\n')
	case float64:
		b.WriteString(strconv.FormatFloat(v, 'g', -1, 64))
		b.WriteByte('\n')
	case nil:
	default:
		// Anything YAML or JSON can produce that is not listed above. Ignoring it
		// would silently make part of a body unsearchable, which is worse than a
		// %v that occasionally reads oddly.
		b.WriteString(strings.ToLower(fmt.Sprint(v)))
		b.WriteByte('\n')
	}
}

// Matcher tests requests against one query, holding the query's lowercased form
// so that filtering a list is not a case fold per entry.
//
// It matches on whitespace-separated terms, all of which must appear somewhere
// in the entry: "sayhello alice" finds the SayHello that carried alice without
// caring which came first. An empty query matches everything, which is what a
// filter box nobody has typed into should do.
type Matcher struct {
	terms []string
}

// NewMatcher compiles a filter query.
func NewMatcher(query string) Matcher {
	return Matcher{terms: strings.Fields(strings.ToLower(query))}
}

// Empty reports whether the query would match everything.
func (m Matcher) Empty() bool { return len(m.terms) == 0 }

// Match reports whether a haystack from [Request.Search] satisfies the query.
func (m Matcher) Match(haystack string) bool {
	for _, term := range m.terms {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}
