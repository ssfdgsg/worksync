package manifest

import (
	"fmt"
	"strings"
)

// ExpandVars expands explicit "${VAR}" references in s using lookup.
// Bare "$VAR" is left literally untouched; only the bracketed form is
// recognized (design §12.2: expansion must be explicit). A malformed
// reference such as "${" returns an error. Unset variables are returned in
// missing so callers can decide how loudly to fail.
func ExpandVars(s string, lookup func(string) (string, bool)) (expanded string, missing []string, err error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '$' || i+1 >= len(s) || s[i+1] != '{' {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := strings.IndexByte(s[i+2:], '}')
		if end < 0 {
			return "", nil, fmt.Errorf("malformed variable reference at offset %d: missing closing brace", i)
		}
		name := s[i+2 : i+2+end]
		if name == "" {
			return "", nil, fmt.Errorf("malformed variable reference at offset %d: empty name", i)
		}
		val, ok := lookup(name)
		if !ok {
			missing = append(missing, name)
		}
		b.WriteString(val)
		i += 2 + end + 1
	}
	return b.String(), missing, nil
}
