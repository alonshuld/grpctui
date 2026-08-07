package protoschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
)

// jsonIndent is one level of indentation in a rendered response.
const jsonIndent = "  "

// Format is how a response body ended up being rendered.
type Format string

// The formats a response body can come back in.
const (
	// FormatJSON is protobuf's JSON mapping, which is what the response panel
	// shows whenever it can.
	FormatJSON Format = "json"

	// FormatText is protobuf's text format, used only when JSON is impossible.
	FormatText Format = "text"
)

// Marshal renders a decoded response for display, preferring JSON and falling
// back to protobuf's text format when JSON is impossible.
//
// One thing makes it impossible in practice: a google.protobuf.Any whose
// payload type this client has never seen. protojson refuses to render such a
// message at all, while prototext degrades to printing the Any's type URL and
// raw bytes. The call succeeded either way, so failing here would report a
// perfectly good answer as a failed call — and Any is common enough
// (google.rpc.Status details, grpc-gateway) to be worth the fallback.
//
// The returned [Format] says which happened, so the panel can tell the user why
// the body does not look like JSON.
func Marshal(msg proto.Message) (string, Format, error) {
	body, err := MarshalJSON(msg)
	if err == nil {
		return body, FormatJSON, nil
	}
	if msg == nil {
		return "", FormatJSON, err
	}

	text, textErr := prototext.MarshalOptions{Multiline: true, Indent: jsonIndent}.Marshal(msg)
	if textErr != nil {
		// Both renderings failed, which says something is wrong with the
		// message rather than with JSON. The JSON error is the more specific of
		// the two, so it is the one worth showing.
		return "", FormatJSON, err
	}
	return string(text), FormatText, nil
}

// MarshalJSON renders a decoded response message as indented JSON. It is
// strict: a message JSON cannot represent is an error. Callers rendering a
// response for the user want [Marshal], which falls back rather than failing.
//
// It deliberately does not ask protojson for the indentation. protojson
// randomises its whitespace on purpose — the amount of space after a key varies
// between builds of the same program — so its multiline output cannot be
// compared in a golden file and looks arbitrary next to hand-written JSON.
// Marshalling compact and re-indenting with encoding/json gives byte-stable
// output while keeping protobuf's JSON mapping (field name casing, 64-bit ints
// as strings, well-known types) intact.
//
// Unpopulated fields are emitted: the point of the response panel is to show
// what the server actually returned, and a field that came back at its default
// is information, not noise.
func MarshalJSON(msg proto.Message) (string, error) {
	if msg == nil {
		return "", errors.New("marshal response: no message")
	}

	compact, err := protojson.MarshalOptions{EmitUnpopulated: true}.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("marshal response as JSON: %w", err)
	}

	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", jsonIndent); err != nil {
		return "", fmt.Errorf("indent response JSON: %w", err)
	}
	return out.String(), nil
}
