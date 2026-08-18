package commit

import (
	"bytes"
	"encoding/json"
)

// MarshalCanonical serializes v as canonical JSON: objects with keys sorted
// lexicographically, minimal separators, no trailing newline, so that the
// byte representation (and therefore the SHA-256 digest) is deterministic
// across implementations (design §14.1).
func MarshalCanonical(v interface{}) ([]byte, error) {
	// Round-trip through json to normalize types (e.g. time.Time to RFC3339
	// strings), then canonicalize key order.
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var anyVal interface{}
	if err := json.Unmarshal(b, &anyVal); err != nil {
		return nil, err
	}
	return json.Marshal(canonicalize(anyVal))
}

// canonicalize recursively normalizes a decoded JSON value. encoding/json
// already emits map[string]interface{} keys in lexicographic order, so the
// main work is ensuring the tree shape is stable.
func canonicalize(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			out[k] = canonicalize(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, val := range t {
			out[i] = canonicalize(val)
		}
		return out
	default:
		return v
	}
}

// newBytesReader avoids importing bytes in the descriptor file.
func newBytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
