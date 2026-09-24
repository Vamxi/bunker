// tomledit.go - comment-preserving edits of flat TOML keys.
//
// The settings window changes one key at a time. Re-encoding the whole file
// would drop the user's comments and ordering, so setTOMLKey rewrites only
// the value on the key's line, keeping indentation and any trailing comment.
// It handles the subset bunker writes: top-level keys and keys in [section]
// tables, with string, number, and boolean values.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// writeConfigKey sets section.key = literal in the config file at path,
// creating it from the documented template when missing. The edited text is
// validated before it replaces the file, and the replace is an atomic
// rename. It reports whether the file changed.
func writeConfigKey(path, section, key, literal string) (bool, error) {
	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		data = []byte(DefaultConfigTOML())
	case err != nil:
		return false, err
	}
	next := setTOMLKey(string(data), section, key, literal)
	if next == string(data) {
		return false, nil
	}
	var check fileConfig
	if _, err := toml.Decode(next, &check); err != nil {
		return false, fmt.Errorf("refusing to write invalid config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(next), 0o600); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return false, err
	}
	return true, nil
}

// setTOMLKey returns content with section.key set to literal (an encoded
// TOML value, e.g. `"nord"` or `12`). section "" is the top level.
//
//   - An existing assignment is rewritten in place, trailing comment kept.
//   - Otherwise the key goes right after a commented-out `# key = …` line
//     in that section if there is one, else at the end of the section
//     (top-level keys go before the first table header).
//   - A missing section is appended at the end of the file.
func setTOMLKey(content, section, key, literal string) string {
	lines := strings.Split(content, "\n")
	cur := ""
	sectionFound := section == ""
	insertAt := -1   // index to insert a new line before
	commentHit := -1 // "# key =" line inside the section

	for i, raw := range lines {
		trim := strings.TrimSpace(raw)
		if name, ok := tomlTableHeader(trim); ok {
			if cur == section && insertAt < 0 {
				insertAt = lastSectionLine(lines, i) + 1
			}
			cur = name
			if cur == section {
				sectionFound = true
			}
			continue
		}
		if cur != section {
			continue
		}
		if k, valStart, ok := tomlAssignment(raw); ok && k == key {
			end := tomlValueEnd(raw, valStart)
			lines[i] = raw[:valStart] + literal + raw[end:]
			return strings.Join(lines, "\n")
		}
		if commentHit < 0 && strings.HasPrefix(trim, "#") {
			if k, _, ok := tomlAssignment(strings.TrimSpace(strings.TrimPrefix(trim, "#"))); ok && k == key {
				commentHit = i
			}
		}
	}

	newLine := key + " = " + literal
	switch {
	case commentHit >= 0:
		insertAt = commentHit + 1
	case !sectionFound:
		body := strings.TrimRight(content, "\n")
		if body != "" {
			body += "\n\n"
		}
		return body + "[" + section + "]\n" + newLine + "\n"
	case insertAt < 0: // section runs to end of file
		insertAt = lastContentLine(lines, len(lines)) + 1
	}
	lines = append(lines[:insertAt], append([]string{newLine}, lines[insertAt:]...)...)
	return strings.Join(lines, "\n")
}

// tomlTableHeader reports "[name]" headers (not [[array]] tables).
func tomlTableHeader(trim string) (string, bool) {
	if !strings.HasPrefix(trim, "[") || strings.HasPrefix(trim, "[[") {
		return "", false
	}
	end := strings.IndexByte(trim, ']')
	if end < 0 {
		return "", false
	}
	return strings.TrimSpace(trim[1:end]), true
}

// tomlAssignment parses `key = value` and returns the key and the byte
// offset where the value begins (after `=` and spaces).
func tomlAssignment(line string) (key string, valStart int, ok bool) {
	eq := strings.IndexByte(line, '=')
	if eq <= 0 {
		return "", 0, false
	}
	key = strings.TrimSpace(line[:eq])
	if key == "" || strings.ContainsAny(key, " \t#[\"'") {
		return "", 0, false
	}
	valStart = eq + 1
	for valStart < len(line) && (line[valStart] == ' ' || line[valStart] == '\t') {
		valStart++
	}
	return key, valStart, true
}

// tomlValueEnd finds where the value starting at start ends: after the
// closing quote for strings, else before whitespace or a comment.
func tomlValueEnd(line string, start int) int {
	if start >= len(line) {
		return start
	}
	switch q := line[start]; q {
	case '"', '\'':
		for i := start + 1; i < len(line); i++ {
			if q == '"' && line[i] == '\\' {
				i++
				continue
			}
			if line[i] == q {
				return i + 1
			}
		}
		return len(line)
	}
	end := start
	for end < len(line) && line[end] != ' ' && line[end] != '\t' && line[end] != '#' {
		end++
	}
	return end
}

// lastSectionLine is the last line of a section that ends at a table header:
// blank lines and the comment block introducing the next table are skipped,
// so a new key does not land under the next table's heading comment.
func lastSectionLine(lines []string, header int) int {
	i := header - 1
	for i >= 0 {
		trim := strings.TrimSpace(lines[i])
		if trim != "" && !strings.HasPrefix(trim, "#") {
			break
		}
		i--
	}
	return i
}

// lastContentLine is the last non-blank line index before limit, so new keys
// land under the section's existing lines rather than after blank padding.
func lastContentLine(lines []string, limit int) int {
	for i := limit - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return i
		}
	}
	return -1
}

// tomlString encodes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			b.WriteString(`\u00`)
			b.WriteString(strconv.FormatInt(int64(r)+0x100, 16)[1:])
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
