package config

import (
	"fmt"
	"strconv"
	"strings"
)

// parseTOML reads the subset of TOML this config needs: `key = value` lines
// at the top level and under `[table]` / `[table.sub]` headers, where a value
// is a quoted string, an integer, a boolean, an array of quoted strings on
// one line, or an inline table of quoted strings on one line. It answers a
// nested map, which is then rendered as JSON and
// decoded through the same struct tags the document has always used — so
// every default, every refusal and every field name is the one the config
// already had, and only the surface syntax moved.
//
// Hand-written, because the dependency budget is one library and it is the
// MCP go-sdk. What is NOT accepted is refused by line number rather than
// ignored: multi-line arrays and inline tables, an inline table holding
// anything but quoted strings, a bare value this parser cannot type. A
// setting an operator wrote and this binary silently dropped is the
// failure mode a config parser exists to prevent, and it is the one a
// permissive parser produces.
func parseTOML(src string) (map[string]any, error) {
	root := map[string]any{}
	table := root
	for i, line := range strings.Split(src, "\n") {
		n := i + 1
		s := strings.TrimSpace(stripComment(line))
		if s == "" {
			continue
		}
		if strings.HasPrefix(s, "[[") {
			if !strings.HasSuffix(s, "]]") {
				return nil, fmt.Errorf("line %d: an array-of-tables header must open and close on one line: %q", n, s)
			}
			next, err := appendTable(root, strings.TrimSpace(s[2:len(s)-2]))
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", n, err)
			}
			table = next
			continue
		}
		if strings.HasPrefix(s, "[") {
			if !strings.HasSuffix(s, "]") {
				return nil, fmt.Errorf("line %d: a table header must open and close on one line: %q", n, s)
			}
			next, err := descend(root, strings.TrimSpace(s[1:len(s)-1]))
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", n, err)
			}
			table = next
			continue
		}
		key, rest, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected `key = value`, got %q", n, s)
		}
		key, rest = strings.TrimSpace(key), strings.TrimSpace(rest)
		if key == "" || rest == "" {
			return nil, fmt.Errorf("line %d: expected `key = value`, got %q", n, s)
		}
		name, err := bareKey(key)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		v, err := parseValue(rest)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		if _, dup := table[name]; dup {
			return nil, fmt.Errorf("line %d: %q is set twice, and which one wins is not something a config should leave to the reader", n, name)
		}
		table[name] = v
	}
	return root, nil
}

// descend walks a dotted table header, creating the tables on the way. A path
// that runs into something already set to a scalar is refused rather than
// overwritten.
func descend(root map[string]any, path string) (map[string]any, error) {
	if path == "" {
		return nil, fmt.Errorf("a table header names nothing")
	}
	at := root
	for _, part := range splitTopLevel(path, '.') {
		name, err := bareKey(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		switch held := at[name].(type) {
		case nil:
			next := map[string]any{}
			at[name] = next
			at = next
		case map[string]any:
			at = held
		default:
			return nil, fmt.Errorf("%q is already a value, so it cannot also be a table", name)
		}
	}
	return at, nil
}

// appendTable adds one more entry to an array of tables and answers it. The
// path is walked the same way a `[table.sub]` header is, and only the LAST
// name becomes the list: everything before it is an ordinary table.
//
// A name already holding something that is not a list of tables is refused
// rather than replaced, for the reason every refusal in this parser exists:
// the alternative is an operator's keys landing somewhere they never wrote.
func appendTable(root map[string]any, path string) (map[string]any, error) {
	if path == "" {
		return nil, fmt.Errorf("a table header names nothing")
	}
	parts := splitTopLevel(path, '.')
	parent := root
	if len(parts) > 1 {
		var err error
		parent, err = descend(root, strings.Join(parts[:len(parts)-1], "."))
		if err != nil {
			return nil, err
		}
	}
	name, err := bareKey(strings.TrimSpace(parts[len(parts)-1]))
	if err != nil {
		return nil, err
	}
	held, set := parent[name]
	if !set {
		held = []any{}
	}
	list, ok := held.([]any)
	if !ok {
		return nil, fmt.Errorf("%q is already a value, so it cannot also be an array of tables", name)
	}
	next := map[string]any{}
	parent[name] = append(list, next)
	return next, nil
}

// bareKey is a key as written: a bare word, or a quoted one, which is how a
// project path becomes a key.
func bareKey(k string) (string, error) {
	if strings.HasPrefix(k, `"`) {
		out, err := strconv.Unquote(k)
		if err != nil {
			return "", fmt.Errorf("%s is not a readable key", k)
		}
		return out, nil
	}
	if k == "" || strings.ContainsAny(k, " \t[]{}\"") {
		return "", fmt.Errorf("%q is not a key: quote it if it is a path", k)
	}
	return k, nil
}

// parseValue types one right-hand side. Anything it cannot type is an error
// naming the text, never a string it guessed at.
func parseValue(s string) (any, error) {
	switch {
	case strings.HasPrefix(s, `"`):
		out, err := strconv.Unquote(s)
		if err != nil {
			return nil, fmt.Errorf("%s is not a readable string", s)
		}
		return out, nil
	case s == "true":
		return true, nil
	case s == "false":
		return false, nil
	case strings.HasPrefix(s, "["):
		return parseList(s)
	case strings.HasPrefix(s, "{"):
		return parseInlineTable(s)
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil, fmt.Errorf("%s is not a quoted string, a whole number, true or false", s)
	}
	return n, nil
}

// parseInlineTable reads `{ KEY = "value", ... }` on ONE line, where every
// value is a quoted string.
//
// It is the narrowest inline table that carries a worker's environment, which
// is the only setting here shaped like a map of names to strings: written as
// `[worker.env]` it would be a header an operator has to place after every
// other key of its table, and a `[profiles.x.env]` under it again.
//
// Everything else an inline table can hold is still refused by line, the way
// the rest of this parser refuses: a value that is not a quoted string, a
// nested table, a table that does not close on its line. So `layout = {
// min_pane_columns = 40 }` is the error it always was.
func parseInlineTable(s string) (map[string]any, error) {
	if !strings.HasSuffix(s, "}") {
		return nil, fmt.Errorf("an inline table must open and close on one line: %s", s)
	}
	out := map[string]any{}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return out, nil
	}
	for _, pair := range splitTopLevel(inner, ',') {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		rawKey, rawValue, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("an inline table holds `key = \"value\"` pairs, and %q is not one", pair)
		}
		name, err := bareKey(strings.TrimSpace(rawKey))
		if err != nil {
			return nil, err
		}
		value, err := strconv.Unquote(strings.TrimSpace(rawValue))
		if err != nil {
			return nil, fmt.Errorf("an inline table's values must be quoted strings: %s", strings.TrimSpace(rawValue))
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("%q is set twice in one inline table, and which one wins is not something a config should leave to the reader", name)
		}
		out[name] = value
	}
	return out, nil
}

func parseList(s string) ([]any, error) {
	if !strings.HasSuffix(s, "]") {
		return nil, fmt.Errorf("an array must open and close on one line: %s", s)
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	out := []any{}
	if inner == "" {
		return out, nil
	}
	for _, p := range splitTopLevel(inner, ',') {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.Unquote(p)
		if err != nil {
			return nil, fmt.Errorf("array elements must be quoted strings: %s", p)
		}
		out = append(out, v)
	}
	return out, nil
}

// stripComment drops a trailing `# ...`, but only outside quotes: a project
// path or an argv word may carry a hash.
func stripComment(s string) string {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case '#':
			if !inQuote {
				return s[:i]
			}
		}
	}
	return s
}

// splitTopLevel splits on sep outside quotes.
func splitTopLevel(s string, sep byte) []string {
	out := []string{}
	inQuote := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case sep:
			if !inQuote {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}
