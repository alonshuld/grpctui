package grpcclient

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc/metadata"
)

// ErrReservedHeader reports a header key gRPC keeps for itself. Sending one
// either does nothing or corrupts the call, so the form refuses it rather than
// letting the user wonder why their header never arrived.
var ErrReservedHeader = errors.New("reserved for gRPC")

// binarySuffix marks a header whose value is binary. gRPC base64-encodes such a
// value on the wire, so what the user types is the encoded form and what we
// hand to the metadata package is the decoded bytes — otherwise it is encoded
// twice and the server sees gibberish.
const binarySuffix = "-bin"

// Header is one request metadata entry: an HTTP/2 header sent with the RPC.
type Header struct {
	// Key is the header name. gRPC lowercases keys on the wire; grpctui does it
	// on the way in, so what the panel shows is what the server receives.
	Key string

	// Value is the header value. For a "-bin" key it is the base64 form.
	Value string

	// Disabled leaves the header in the panel but off the wire, so a header can
	// be parked without retyping it later.
	Disabled bool
}

// Metadata is the request metadata sent with an RPC, in the order the user
// wrote it. Duplicate keys are legal — gRPC metadata is multi-valued — and are
// sent in order.
type Metadata []Header

// ValidateHeader checks one header, returning the reason it cannot be sent.
//
// The rules are gRPC's: a key is a lowercase HTTP/2 header name, the "grpc-"
// space belongs to the protocol, and a "-bin" value has to be base64 because
// that is how it travels.
func ValidateHeader(h Header) error {
	key := strings.TrimSpace(h.Key)
	switch {
	case key == "":
		return errors.New("empty header key")
	case strings.HasPrefix(key, ":"):
		return fmt.Errorf("%q is an HTTP/2 pseudo-header: %w", key, ErrReservedHeader)
	case strings.HasPrefix(strings.ToLower(key), "grpc-"):
		return fmt.Errorf("%q is %w", key, ErrReservedHeader)
	}

	for _, r := range key {
		if !validKeyRune(r) {
			return fmt.Errorf("invalid character %q in header key %q", r, key)
		}
	}

	if isBinaryKey(key) {
		if _, err := decodeBinary(h.Value); err != nil {
			return fmt.Errorf("%q is a binary header, so its value must be base64: %w", key, err)
		}
	}
	return nil
}

// Validate checks every enabled header, reporting all the problems at once
// rather than the first: a call refused for two bad headers should say so
// twice.
func (md Metadata) Validate() error {
	var errs []error
	for _, h := range md {
		if h.Disabled {
			continue
		}
		if err := ValidateHeader(h); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Enabled returns the headers that will actually be sent.
func (md Metadata) Enabled() Metadata {
	out := make(Metadata, 0, len(md))
	for _, h := range md {
		if !h.Disabled {
			out = append(out, h)
		}
	}
	return out
}

// Clone returns a copy that can be edited without disturbing the original —
// what a connection profile hands to the panel that lets the user change it.
func (md Metadata) Clone() Metadata {
	if md == nil {
		return nil
	}
	out := make(Metadata, len(md))
	copy(out, md)
	return out
}

// Keys lists the enabled header names. It exists for everything that records a
// call without being the call: the log, and since v0.6 the history and
// collection files. The names of the headers on a call are useful there, and
// the values — bearer tokens, API keys — must never be.
func (md Metadata) Keys() []string {
	keys := make([]string, 0, len(md))
	for _, h := range md {
		if !h.Disabled {
			keys = append(keys, normalizeKey(h.Key))
		}
	}
	return keys
}

// attach returns ctx carrying md as outgoing gRPC metadata. Disabled headers
// are left behind; an invalid one is an error, because silently dropping a
// header the user typed is how an afternoon disappears.
func (md Metadata) attach(ctx context.Context) (context.Context, error) {
	if len(md) == 0 {
		return ctx, nil
	}

	pairs := make([]string, 0, len(md)*2)
	for _, h := range md {
		if h.Disabled {
			continue
		}
		if err := ValidateHeader(h); err != nil {
			return nil, fmt.Errorf("header %q: %w", h.Key, err)
		}

		key := normalizeKey(h.Key)
		value := h.Value
		if isBinaryKey(key) {
			raw, err := decodeBinary(value)
			if err != nil {
				return nil, fmt.Errorf("header %q: %w", key, err)
			}
			value = string(raw)
		}
		pairs = append(pairs, key, value)
	}

	if len(pairs) == 0 {
		return ctx, nil
	}
	return metadata.AppendToOutgoingContext(ctx, pairs...), nil
}

func normalizeKey(key string) string { return strings.ToLower(strings.TrimSpace(key)) }

func isBinaryKey(key string) bool { return strings.HasSuffix(normalizeKey(key), binarySuffix) }

// decodeBinary accepts both base64 alphabets, padded or not: what the user
// pasted came from somewhere else, and which of the four spellings it uses is
// not something they should have to know.
func decodeBinary(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	encodings := []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	}

	var err error
	for _, enc := range encodings {
		var raw []byte
		if raw, err = enc.DecodeString(value); err == nil {
			return raw, nil
		}
	}
	return nil, err
}

// validKeyRune reports whether r may appear in a header key. The set is
// HTTP/2's token characters, narrowed to what gRPC actually accepts.
func validKeyRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '-', r == '_', r == '.':
		return true
	default:
		return false
	}
}
