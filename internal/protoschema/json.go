package protoschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// jsonIndent is one level of indentation in a rendered response.
const jsonIndent = "  "

// MarshalJSON renders a decoded response message as indented JSON.
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
