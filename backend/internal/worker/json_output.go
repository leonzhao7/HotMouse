package worker

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var responseFence = regexp.MustCompile("(?is)\x60\x60\x60(?:json)?[ \\t]*\\r?\\n(.*?)\\r?\\n[ \\t]*\x60\x60\x60")

func decodeObject(raw []byte, out *map[string]json.RawMessage) error {
	segment := strings.TrimSpace(string(raw))
	if segment == "" {
		return fmt.Errorf("JSON object not found")
	}
	if !strings.HasPrefix(segment, "{") && !strings.HasPrefix(segment, "[") {
		matches := responseFence.FindAllStringSubmatchIndex(segment, -1)
		if len(matches) > 0 {
			m := matches[0]
			if hasJSONResponse(segment[:m[0]]) || hasJSONResponse(segment[m[1]:]) {
				return fmt.Errorf("ambiguous JSON outside response fence")
			}
			segment = strings.TrimSpace(segment[m[2]:m[3]])
		} else if strings.HasPrefix(segment, "\x60\x60\x60") {
			return fmt.Errorf("unclosed JSON response fence")
		} else {
			start := strings.IndexByte(segment, '{')
			if start < 0 {
				return fmt.Errorf("JSON object not found")
			}
			// Do not extract a nested object from a prose-prefixed JSON array.
			for offset, char := range segment[:start] {
				if char != '[' {
					continue
				}
				decoder := json.NewDecoder(strings.NewReader(segment[offset:]))
				var response json.RawMessage
				if decoder.Decode(&response) == nil && decoder.InputOffset() > int64(start-offset) {
					return fmt.Errorf("JSON response must be an object")
				}
			}
			segment = segment[start:]
		}
	}
	decoder := json.NewDecoder(strings.NewReader(segment))
	var response json.RawMessage
	if err := decoder.Decode(&response); err != nil {
		return fmt.Errorf("invalid JSON response")
	}
	remainder := strings.TrimSpace(segment[decoder.InputOffset():])
	if hasJSONResponse(remainder) {
		return fmt.Errorf("unexpected content after JSON response")
	}
	if len(response) == 0 || response[0] != '{' {
		return fmt.Errorf("JSON response must be an object")
	}
	if err := uniqueJSONKeys(json.NewDecoder(strings.NewReader(string(response)))); err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(response, &object); err != nil {
		return fmt.Errorf("invalid JSON object")
	}
	*out = object
	return nil
}

// A complete object or array starting the remaining output or a later line is
// another response. Braces, inline examples and Markdown in explanatory prose
// do not make the selected JSON ambiguous.
func hasJSONResponse(raw string) bool {
	for raw != "" {
		candidate := strings.TrimLeft(raw, " \t\r")
		if strings.HasPrefix(candidate, "{") || strings.HasPrefix(candidate, "[") {
			var response json.RawMessage
			if json.NewDecoder(strings.NewReader(candidate)).Decode(&response) == nil {
				return true
			}
		}
		newline := strings.IndexByte(raw, '\n')
		if newline < 0 {
			break
		}
		raw = raw[newline+1:]
	}
	return false
}

func uniqueJSONKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("invalid JSON response")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		keys := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("invalid JSON object key")
			}
			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("invalid JSON object key")
			}
			if keys[name] {
				return fmt.Errorf("duplicate JSON object key")
			}
			keys[name] = true
			if err := uniqueJSONKeys(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := uniqueJSONKeys(decoder); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("invalid JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}
