package protoschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alonshuld/grpctui/internal/protoschema"
)

// The body a capture reads is the JSON the response panel is showing, which is
// protobuf's JSON mapping: 64-bit integers as strings, bytes as base64,
// lowerCamelCase names.
const captureBody = `{
  "user": {
    "id": "42",
    "name": "alice",
    "verified": true,
    "score": 1.5,
    "retryCount": 3,
    "nickname": null
  },
  "items": [
    {"sku": "aaa"},
    {"sku": "bbb"}
  ],
  "tags": ["x", "y"],
  "accessToken": "ey.token"
}`

func TestLookupJSON(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "top level", path: "accessToken", want: "ey.token"},
		{name: "nested string", path: "user.name", want: "alice"},
		{name: "int64 comes back as protobuf writes it", path: "user.id", want: "42"},
		{name: "int32 keeps no exponent", path: "user.retryCount", want: "3"},
		{name: "bool", path: "user.verified", want: "true"},
		{name: "float", path: "user.score", want: "1.5"},
		{name: "list of scalars", path: "tags[1]", want: "y"},
		{name: "into a list of messages", path: "items[0].sku", want: "aaa"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := protoschema.LookupJSON(captureBody, tt.path)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLookupJSONRefusals(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		path    string
		message string
	}{
		{name: "no such field", body: captureBody, path: "user.email", message: `no "email"`},
		{name: "no such top-level field", body: captureBody, path: "nope", message: `no "nope"`},
		{name: "past the end of a list", body: captureBody, path: "tags[9]", message: "has 2 items"},
		{name: "indexing something that is not a list", body: captureBody, path: "user[0]", message: "not a list"},
		{name: "descending into a scalar", body: captureBody, path: "user.name.first", message: "not an object"},
		{name: "a whole object", body: captureBody, path: "user", message: "not a single value"},
		{name: "a whole list", body: captureBody, path: "tags", message: "not a single value"},
		{name: "null", body: captureBody, path: "user.nickname", message: "is null"},
		{name: "empty path", body: captureBody, path: "", message: "names nothing"},
		{name: "malformed path", body: captureBody, path: "tags[x]", message: "item index"},
		{name: "not json at all", body: "name: alice\n", path: "name", message: "not JSON"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := protoschema.LookupJSON(tt.body, tt.path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.message)
		})
	}
}

// A capture is only worth having if the path it takes is the one the request
// form already shows, so the two have to agree.
func TestLookupJSONUsesTheSamePathsAsTheForm(t *testing.T) {
	form := testForm(t)
	open(t, form, "nested")

	const body = `{"nested": {"note": "hello"}}`

	got, err := protoschema.LookupJSON(body, row(t, form, "nested.note").Path())
	require.NoError(t, err)
	assert.Equal(t, "hello", got)
}
