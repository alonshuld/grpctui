package protoschema

import (
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// EncodeBody renders a request message as the generic structure a saved request
// carries: maps, slices, strings, numbers and bools, and nothing protobuf.
//
// It exists so that internal/requests can persist a request without knowing
// what a descriptor is, and so that a collection file holds the body as
// ordinary nested YAML keys rather than as one escaped JSON string. A body you
// can read in a diff is most of what makes a collection worth committing.
//
// Unpopulated fields are left out, for the reason [MarshalRequest] leaves them
// out: they are every field of the form the user did not fill in, and a saved
// request that lists all of them is one nobody can read.
func EncodeBody(msg proto.Message) (any, error) {
	if msg == nil {
		return nil, nil
	}

	// protojson's own output goes through encoding/json rather than being used
	// directly: the point is a Go value, and this is the only decoder that agrees
	// with protojson about how 64-bit integers and well-known types are written.
	raw, err := protojson.MarshalOptions{}.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("encode request body: %w", err)
	}

	var body any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("decode request body: %w", err)
	}
	return body, nil
}

// DecodeBody rebuilds a message of the given type from a saved body.
//
// A body that names a field the method does not have is an error rather than
// something to skip. Collections are hand-edited and outlive schema changes, so
// a renamed field has to be reported: quietly sending a request missing the
// field you thought you had set is the failure mode this whole feature exists
// to avoid.
func DecodeBody(md protoreflect.MessageDescriptor, body any) (proto.Message, error) {
	if md == nil {
		return nil, errors.New("decode request body: no message type")
	}

	msg := dynamicpb.NewMessage(md)
	if body == nil {
		return msg, nil
	}

	// YAML decodes a mapping into map[string]any when every key is a string and
	// into map[any]any when one is not, and encoding/json can only marshal the
	// first. Normalising here means a hand-written collection with an integer map
	// key gets protobuf's complaint about the field rather than json's complaint
	// about the type.
	normalised, err := normalizeBody(body)
	if err != nil {
		return nil, fmt.Errorf("decode request body: %w", err)
	}

	raw, err := json.Marshal(normalised)
	if err != nil {
		return nil, fmt.Errorf("decode request body: %w", err)
	}
	if err := (protojson.UnmarshalOptions{}).Unmarshal(raw, msg); err != nil {
		return nil, fmt.Errorf("decode request body for %s: %w", md.FullName(), err)
	}
	return msg, nil
}

// normalizeBody converts a YAML-decoded value into one encoding/json can
// marshal, turning every mapping into a string-keyed one.
func normalizeBody(body any) (any, error) {
	switch v := body.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, value := range v {
			normalised, err := normalizeBody(value)
			if err != nil {
				return nil, err
			}
			out[key] = normalised
		}
		return out, nil

	case map[any]any:
		out := make(map[string]any, len(v))
		for key, value := range v {
			name, ok := key.(string)
			if !ok {
				// protobuf's JSON mapping writes even an integer map key as a
				// string, so a key that is not one cannot have come from a message
				// and cannot be turned into one.
				return nil, fmt.Errorf("field name %v is not a string", key)
			}
			normalised, err := normalizeBody(value)
			if err != nil {
				return nil, err
			}
			out[name] = normalised
		}
		return out, nil

	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			normalised, err := normalizeBody(item)
			if err != nil {
				return nil, err
			}
			out[i] = normalised
		}
		return out, nil

	default:
		return v, nil
	}
}
