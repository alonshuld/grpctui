package styles

import "strings"

// HighlightJSON colours a rendered JSON document for the response panel.
//
// It is a highlighter, not a parser: it never rearranges, re-indents or
// validates anything, and every byte it is given comes back out in the order it
// arrived. That is what lets the response panel show exactly what the server
// answered — the body is already indented by protoschema, and a highlighter
// that reflowed it would be quietly editing the response.
//
// Input that is not JSON degrades to plain text rather than to nonsense, which
// matters because a truncated or malformed body is precisely the sort of thing
// a debugging tool gets pointed at.
func (s Styles) HighlightJSON(src string) string {
	var out strings.Builder
	out.Grow(len(src))

	for _, tok := range scanJSON(src) {
		switch tok.class {
		case jsonKey:
			out.WriteString(s.JSONKey.Render(tok.text))
		case jsonString:
			out.WriteString(s.JSONString.Render(tok.text))
		case jsonNumber:
			out.WriteString(s.JSONNumber.Render(tok.text))
		case jsonLiteral:
			out.WriteString(s.JSONLiteral.Render(tok.text))
		case jsonPunct:
			out.WriteString(s.JSONPunct.Render(tok.text))
		default:
			out.WriteString(tok.text)
		}
	}
	return out.String()
}

// jsonClass is what a run of characters in a JSON document is.
type jsonClass int

const (
	// jsonPlain is whitespace and anything the scanner does not recognise. It is
	// rendered unstyled, which is how malformed input stays readable.
	jsonPlain jsonClass = iota
	jsonKey
	jsonString
	jsonNumber
	jsonLiteral
	jsonPunct
)

// jsonToken is one run of characters with one class.
type jsonToken struct {
	text  string
	class jsonClass
}

// scanJSON splits a JSON document into coloured runs.
//
// It exists as its own function so that the classification can be tested for
// what it is: lipgloss renders styles away to nothing on a terminal without
// colour, which is every terminal a test runs in.
func scanJSON(src string) []jsonToken {
	var tokens []jsonToken
	start := 0

	// flush emits everything since the last token as unclassified text.
	flush := func(end int) {
		if end > start {
			tokens = append(tokens, jsonToken{text: src[start:end], class: jsonPlain})
		}
	}

	for i := 0; i < len(src); {
		switch c := src[i]; {
		case c == '"':
			end := endOfString(src, i)
			flush(i)
			tokens = append(tokens, jsonToken{text: src[i:end], class: stringClass(src, end)})
			i, start = end, end

		case c == '-' || (c >= '0' && c <= '9'):
			end := endOfNumber(src, i)
			flush(i)
			tokens = append(tokens, jsonToken{text: src[i:end], class: jsonNumber})
			i, start = end, end

		case strings.ContainsRune("{}[],:", rune(c)):
			flush(i)
			tokens = append(tokens, jsonToken{text: src[i : i+1], class: jsonPunct})
			i++
			start = i

		default:
			if word := literalAt(src, i); word != "" {
				flush(i)
				tokens = append(tokens, jsonToken{text: word, class: jsonLiteral})
				i += len(word)
				start = i
				continue
			}
			i++
		}
	}

	flush(len(src))
	return tokens
}

// endOfString returns the index just past the string starting at i, or the end
// of the input for a string nobody closed.
func endOfString(src string, i int) int {
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++ // an escaped character cannot end the string, whatever it is
		case '"':
			return j + 1
		}
	}
	return len(src)
}

// stringClass tells a key from a value: in JSON the difference is entirely in
// what follows, so it is decided by looking for the colon.
func stringClass(src string, end int) jsonClass {
	for j := end; j < len(src); j++ {
		switch src[j] {
		case ' ', '\t', '\n', '\r':
			continue
		case ':':
			return jsonKey
		default:
			return jsonString
		}
	}
	return jsonString
}

// endOfNumber returns the index just past the number starting at i. It accepts
// more than JSON does — "1.2.3" is one run — because a highlighter that split
// malformed input into pieces would only make it harder to read.
func endOfNumber(src string, i int) int {
	j := i + 1
	for ; j < len(src); j++ {
		c := src[j]
		if (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-' {
			continue
		}
		break
	}
	return j
}

// literalAt returns the JSON keyword starting at i, if there is one.
func literalAt(src string, i int) string {
	for _, word := range []string{"true", "false", "null"} {
		if strings.HasPrefix(src[i:], word) {
			return word
		}
	}
	return ""
}
