package forms

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
)

// Canonical returns the canonical bytes of a definition: compact JSON with
// object keys sorted, no HTML escaping, numbers exactly as written (except
// that a negative zero loses its sign), and a final newline. Exporting the
// same definition always gives the same bytes, so an exported file imports
// and exports again byte for byte. The input must be valid JSON; it is not
// validated as a definition here.
func Canonical(raw []byte) ([]byte, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, &Error{Kind: KindJSON, Msg: "invalid JSON: trailing data"}
	}
	v = canonicalNumbers(v)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil { // Encode sorts map keys and adds "\n"
		return nil, err
	}
	return buf.Bytes(), nil
}

// Digest is the hex SHA-256 of the canonical bytes.
func Digest(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func canonicalNumbers(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = canonicalNumbers(e)
		}
	case []any:
		for i, e := range x {
			x[i] = canonicalNumbers(e)
		}
	case json.Number:
		s := string(x)
		if strings.HasPrefix(s, "-") && strings.Trim(s[1:], "0.") == "" {
			return json.Number(s[1:])
		}
	}
	return v
}
