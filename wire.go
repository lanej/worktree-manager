package main

import (
	"bytes"
	"encoding/json"
	"errors"
)

// decodeWire keeps filesystem surrogate escapes intact when composing native JSON reports.
func decodeWire(data []byte) (any, error) {
	data = bytes.TrimSpace(data)
	if !json.Valid(data) {
		return nil, errors.New("incomplete or invalid JSON response")
	}

	switch data[0] {
	case '{':
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}

		result := map[string]any{}

		for key, value := range raw {
			decoded, err := decodeWire(value)
			if err != nil {
				return nil, err
			}

			result[key] = decoded
		}

		return result, nil
	case '[':
		var raw []json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}

		result := make([]any, len(raw))
		for i, value := range raw {
			decoded, err := decodeWire(value)
			if err != nil {
				return nil, err
			}

			result[i] = decoded
		}

		return result, nil
	case '"':
		var text gitText
		if err := text.UnmarshalJSON(data); err != nil {
			return nil, err
		}

		return string(text), nil
	default:
		var result any

		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()

		if err := decoder.Decode(&result); err != nil {
			return nil, err
		}

		return result, nil
	}
}
