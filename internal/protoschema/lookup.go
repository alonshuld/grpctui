package protoschema

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// LookupJSON reads one value out of a rendered response body by path.
//
// It is what makes a chained request possible: the id a create call returned,
// or the token a login call issued, becomes a variable the next request refers
// to. The body is the JSON the response panel is already showing rather than
// the message it came from, so nothing has to be kept alive between the call
// and the user deciding to capture something out of it.
//
// The path is the same one [Node.Path] writes — "user.id", "items[0].name" —
// which is deliberate: a user who has read one has learnt the other. Only a
// single value can be captured; a path that lands on an object or a list is an
// error saying so, because a variable is text and there is no useful text to
// make of a subtree.
func LookupJSON(body, path string) (string, error) {
	var decoded any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		return "", fmt.Errorf("this response is not JSON, so there is nothing to capture from: %w", err)
	}

	segments := splitPath(path)
	if len(segments) == 0 {
		return "", fmt.Errorf("%q names nothing in the response", path)
	}

	at := decoded
	for _, seg := range segments {
		name, indices, err := parseSegment(seg)
		if err != nil {
			return "", err
		}

		object, ok := at.(map[string]any)
		if !ok {
			return "", fmt.Errorf("%s: %q is not an object", path, name)
		}
		if at, ok = object[name]; !ok {
			return "", fmt.Errorf("%s: the response has no %q", path, name)
		}

		for _, i := range indices {
			list, ok := at.([]any)
			if !ok {
				return "", fmt.Errorf("%s: %q is not a list", path, name)
			}
			if i >= len(list) {
				return "", fmt.Errorf("%s: %q has %d %s", path, name, len(list), plural(len(list), "item"))
			}
			at = list[i]
		}
	}
	return scalarText(at, path)
}

// scalarText renders a captured value as the text a variable holds.
func scalarText(value any, path string) (string, error) {
	switch v := value.(type) {
	case string:
		// protobuf's JSON mapping writes 64-bit integers, bytes and durations as
		// strings, so this is most of what a capture ever lands on.
		return v, nil
	case bool:
		return strconv.FormatBool(v), nil
	case float64:
		// encoding/json decodes every number to a float64. Formatting with -1
		// precision gives back what was written rather than "1e+06" for a round
		// million, which is not something to paste into an int64 field.
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case nil:
		return "", fmt.Errorf("%s is null in this response", path)
	default:
		return "", fmt.Errorf("%s is not a single value: capture a field inside it", path)
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
