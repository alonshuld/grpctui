package proxy

import (
	"fmt"

	"google.golang.org/grpc/encoding"
)

// frame is one message as it travelled: bytes, and nothing else.
type frame struct {
	payload []byte
}

// rawCodec passes messages through without decoding them.
//
// It is what makes a schema-free proxy possible: gRPC insists on a codec, and
// this one satisfies it by doing nothing. A message arrives as bytes, is
// reported as bytes, and leaves as the same bytes — so a method whose
// descriptor grpctui has never seen forwards exactly as well as one it has.
//
// Its name is "proto" rather than something of its own. The name is the
// content-subtype on the wire, and a real client sends
// "application/grpc+proto"; a codec calling itself anything else would not be
// selected for that call.
type rawCodec struct{}

// It is installed per connection through [grpc.ForceCodec] and
// [grpc.ForceServerCodec], never through encoding.RegisterCodec — registering
// it would replace the real protobuf codec process-wide, and grpctui's own
// client is in the same process.
var _ encoding.Codec = rawCodec{}

// Marshal implements [encoding.Codec].
func (rawCodec) Marshal(v any) ([]byte, error) {
	f, ok := v.(*frame)
	if !ok {
		return nil, fmt.Errorf("grpctui proxy: cannot marshal %T", v)
	}
	return f.payload, nil
}

// Unmarshal implements [encoding.Codec].
//
// The bytes are copied. gRPC hands over a buffer it may reuse or return to a
// pool once the call returns, and both the forwarded copy and the event the UI
// reads outlive that moment.
func (rawCodec) Unmarshal(data []byte, v any) error {
	f, ok := v.(*frame)
	if !ok {
		return fmt.Errorf("grpctui proxy: cannot unmarshal into %T", v)
	}

	f.payload = make([]byte, len(data))
	copy(f.payload, data)
	return nil
}

// Name implements [encoding.Codec].
func (rawCodec) Name() string { return "proto" }
