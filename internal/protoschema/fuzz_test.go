package protoschema_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// Build and Load are inverses, and the interesting failures are in the
// formatting: a float that does not survive being printed and read back, a
// number written with spaces around it, a byte string that is not base64 the
// second time. Fuzzing the text going in is the cheapest way to find those,
// since both directions are pure functions over one descriptor.
//
// Values the form refuses are not failures — the point of refusing them is that
// they never reach the wire — so a rejected build simply ends that case.
func FuzzRoundTrip(f *testing.F) {
	f.Add("hello", "12", "1.5", "true", "aGVsbG8=", "COLOUR_RED")
	f.Add("", "-0", "-0.0", "false", "", "0")
	f.Add(" spaced ", "  12  ", "1e-7", "TRUE", "//8=", "99")
	f.Add("\x00￿", "9223372036854775807", "NaN", "1", "AA==", "COLOUR_BLUE")

	f.Fuzz(func(t *testing.T, text, count, ratio, flag, payload, colour string) {
		values := map[string]string{
			"text":    text,
			"count":   count,
			"ratio":   ratio,
			"flag":    flag,
			"payload": payload,
			"colour":  colour,
		}

		form := testForm(t)
		for path, value := range values {
			row(t, form, path).SetValue(value)
		}

		msg, err := form.Build()
		if err != nil {
			return
		}

		reloaded := testForm(t)
		reloaded.Load(msg)

		again, err := reloaded.Build()
		require.NoError(t, err, "a request grpctui built could not be loaded back in")
		require.True(t, proto.Equal(msg, again),
			"reloading changed the request:\nvalues %v\nwant %v\ngot  %v", values, msg, again)
	})
}
