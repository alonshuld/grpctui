// Package export renders a request as a command somebody else can run.
//
// It exists so that a call built in the TUI can leave it — pasted into a bug
// report, a runbook, a chat message — without being rebuilt by hand in
// grpcurl's flags. grpcurl is the target because it is the tool everyone
// already has, and because its flags map almost one-for-one onto a
// [grpcclient.Profile].
//
// # What is never exported
//
// A rendered command carries header *names* and never their values, and names
// the *kind* of credential a connection holds rather than the credential. That
// is grpctui's standing rule — a bearer token, a basic-auth password and a
// header value never reach a surface that outlives the call — and an exported
// command is the most outward-facing surface in the program: it is written to
// be copied somewhere else. What comes out is therefore a command with
// placeholders where the secrets go, which is what you would want to paste into
// a bug report anyway.
//
// {{variable}} references are exported as written, for the same reason: the
// value behind one is very often a token captured out of a login response.
package export

import (
	"slices"
	"strings"

	"github.com/alonshuld/grpctui/internal/grpcclient"
)

// Command is one request to render.
type Command struct {
	// Target is the address to call, "host:port".
	Target string

	// Method is the fully-qualified method name, "package.Service.Method".
	// grpcurl spells the same thing with a slash before the method, which is
	// this package's job to do rather than the caller's.
	Method string

	// Body is the request message as JSON, as the user typed it — with any
	// {{variable}} references still in it.
	Body string

	// Values are the references the JSON could not hold, keyed by field path.
	// A {{name}} in a string field rides along in Body; one in an int64 field
	// cannot, so it is listed in a comment instead of being silently dropped.
	Values map[string]string

	// Headers are the names of the metadata to send. The values are deliberately
	// absent — see the package comment.
	Headers []string

	Security grpcclient.Security
	Auth     grpcclient.Auth
}

// preamble explains the placeholders, so that a command pasted somewhere else
// is not mistaken for one that will run as it stands.
const preamble = "# grpctui does not copy credentials out, so header values below are placeholders."

// Grpcurl renders the command.
//
// The result is a multi-line shell command with backslash continuations: a
// request with three headers on one line is a line nobody can read, and the
// point of exporting one is that a human looks at it.
func Grpcurl(c Command) string {
	lines := []string{preamble}
	lines = append(lines, references(c.Values)...)

	args := []string{"grpcurl"}
	args = append(args, security(c.Security)...)
	args = append(args, headers(c.Headers, c.Auth)...)

	if body := strings.TrimSpace(c.Body); body != "" {
		args = append(args, "-d "+quote(compact(body)))
	}

	// The target and the method are one argument pair rather than two lines:
	// they are the thing being called, and splitting them reads as two separate
	// options.
	args = append(args, quoteIfNeeded(c.Target)+" "+quoteIfNeeded(methodPath(c.Method)))

	return strings.Join(append(lines, join(args)), "\n")
}

// join lays the arguments out one per line with backslash continuations.
func join(args []string) string {
	if len(args) < 2 {
		return strings.Join(args, " ")
	}

	lines := make([]string, 0, len(args))
	lines = append(lines, args[0]+" \\")
	for _, arg := range args[1 : len(args)-1] {
		lines = append(lines, "  "+arg+" \\")
	}
	return strings.Join(append(lines, "  "+args[len(args)-1]), "\n")
}

// security renders the transport flags. grpcurl defaults to TLS and takes
// -plaintext to turn it off, which is the opposite of grpctui's default, so the
// plaintext case is the one that needs a flag.
func security(s grpcclient.Security) []string {
	if !s.TLS {
		return []string{"-plaintext"}
	}

	var args []string
	if s.InsecureSkipVerify {
		args = append(args, "-insecure")
	}
	for _, f := range []struct{ flag, value string }{
		{"-cacert", s.CACert},
		{"-cert", s.ClientCert},
		{"-key", s.ClientKey},
		{"-servername", s.ServerName},
	} {
		if f.value != "" {
			args = append(args, f.flag+" "+quoteIfNeeded(f.value))
		}
	}
	return args
}

// headers renders the metadata flags, the connection's own credential first.
//
// Every value is a placeholder naming what belongs there. A header called
// `authorization` that the connection is already filling in is rendered once,
// from the credential, rather than twice.
func headers(names []string, auth grpcclient.Auth) []string {
	var args []string

	if placeholder, ok := authPlaceholder(auth); ok {
		args = append(args, "-H "+quote("authorization: "+placeholder))
	}

	seen := auth.Kind != grpcclient.AuthNone
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if name == "authorization" && seen {
			continue
		}
		args = append(args, "-H "+quote(name+": <"+name+">"))
	}
	return args
}

// authPlaceholder names the credential a connection carries without revealing
// it, in the shape the header actually takes on the wire — so that somebody
// filling the command in knows whether to write a token or a base64 pair.
func authPlaceholder(a grpcclient.Auth) (string, bool) {
	switch a.Kind {
	case grpcclient.AuthBearer:
		return "Bearer <token>", true
	case grpcclient.AuthBasic:
		user := a.Username
		if user == "" {
			user = "<username>"
		}
		return "Basic <base64 of " + user + ":password>", true
	default:
		return "", false
	}
}

// references lists the {{variable}} references the JSON body could not carry.
//
// They are comments rather than arguments because grpcurl has nowhere to put
// them: the field is at its zero value in the body, and saying so is the only
// honest thing to do with it.
func references(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}

	paths := make([]string, 0, len(values))
	for path := range values {
		paths = append(paths, path)
	}
	// Sorted, so that exporting the same request twice produces the same text
	// and a command pasted into a review does not churn.
	slices.Sort(paths)

	lines := []string{"# these fields hold a {{variable}} the JSON body cannot carry, and are zero above:"}
	for _, path := range paths {
		lines = append(lines, "#   "+path+" = "+values[path])
	}
	return lines
}

// methodPath is grpcurl's spelling of a method: the service, a slash, the
// method. grpctui carries the fully-qualified name with a dot throughout, since
// that is what reflection reports.
func methodPath(fullName string) string {
	if i := strings.LastIndex(fullName, "."); i >= 0 {
		return fullName[:i] + "/" + fullName[i+1:]
	}
	return fullName
}

// compact puts an indented JSON body back on one line.
//
// It drops the whitespace between tokens, which JSON does not care about, and
// leaves everything inside a string literal exactly as it is. It is a squeeze
// rather than a re-encode through encoding/json because the body may hold
// {{variable}} references in places JSON would refuse to parse.
func compact(body string) string {
	var b strings.Builder
	b.Grow(len(body))

	inString, escaped := false, false
	for _, r := range body {
		switch {
		case escaped:
			escaped = false
		case inString && r == '\\':
			escaped = true
		case r == '"':
			inString = !inString
		case !inString && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// quote wraps a value in single quotes, which is the only shell quoting that
// needs no thought about what is inside it — except for a single quote, which
// has to be closed, escaped and reopened.
func quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// quoteIfNeeded quotes only what a shell would otherwise mangle, so that an
// ordinary host:port and method name stay readable.
func quoteIfNeeded(value string) string {
	if value == "" {
		return "''"
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_', r == '/', r == ':', r == '@', r == '+', r == '=':
		default:
			return quote(value)
		}
	}
	return value
}
