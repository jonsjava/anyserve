package steps

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// patchFile changes only the given keys in an existing file's content.
func patchFile(format, content string, set map[string]string) (string, error) {
	switch format {
	case "ini":
		return patchINI(content, set), nil
	case "lines":
		return patchLines(content, set), nil
	case "json":
		return patchJSON(content, set)
	}
	return "", fmt.Errorf("unknown format %q", format)
}

func sortedSet(set map[string]string) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func splitLines(content string) ([]string, string) {
	nl := "\n"
	if strings.Contains(content, "\r\n") {
		nl = "\r\n"
	}
	content = strings.TrimSuffix(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if content == "" {
		return nil, nl
	}
	return strings.Split(content, "\n"), nl
}

// patchINI sets "Section.key" values. The section is everything before the
// last dot, so Unreal-style sections such as /Script/Engine.GameSession work.
func patchINI(content string, set map[string]string) string {
	lines, nl := splitLines(content)
	type want struct{ section, key, value string }
	var wants []want
	for _, k := range sortedSet(set) {
		i := strings.LastIndex(k, ".")
		wants = append(wants, want{k[:i], k[i+1:], set[k]})
	}
	done := make([]bool, len(wants))
	section := ""
	// sectionEnd records the last line index of each section seen.
	sectionEnd := map[string]int{}
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			section = t[1 : len(t)-1]
			sectionEnd[section] = i
			continue
		}
		sectionEnd[section] = i
		eq := strings.Index(t, "=")
		if eq < 0 || strings.HasPrefix(t, ";") || strings.HasPrefix(t, "#") {
			continue
		}
		key := strings.TrimSpace(t[:eq])
		for j, w := range wants {
			if !done[j] && w.section == section && strings.EqualFold(w.key, key) {
				lines[i] = key + "=" + w.value
				done[j] = true
			}
		}
	}
	// Add missing keys at the end of their section, creating it if needed.
	for j, w := range wants {
		if done[j] {
			continue
		}
		if end, ok := sectionEnd[w.section]; ok {
			lines = append(lines[:end+1], append([]string{w.key + "=" + w.value}, lines[end+1:]...)...)
			for s, e := range sectionEnd {
				if e > end || s == w.section {
					sectionEnd[s] = e + 1
				}
			}
			continue
		}
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "["+w.section+"]", w.key+"="+w.value)
		sectionEnd[w.section] = len(lines) - 1
	}
	return strings.Join(lines, nl) + nl
}

// patchLines replaces every line that starts with a key (ignoring leading
// whitespace) with the value, keeping the line's indentation. Keys that
// match nothing are appended.
func patchLines(content string, set map[string]string) string {
	lines, nl := splitLines(content)
	for _, k := range sortedSet(set) {
		prefix := strings.TrimLeft(k, " \t")
		found := false
		for i, line := range lines {
			trimmed := strings.TrimLeft(line, " \t")
			if strings.HasPrefix(trimmed, prefix) {
				indent := line[:len(line)-len(trimmed)]
				lines[i] = indent + strings.TrimLeft(set[k], " \t")
				found = true
			}
		}
		if !found {
			lines = append(lines, set[k])
		}
	}
	return strings.Join(lines, nl) + nl
}

// patchJSON sets dotted paths in a JSON object. A value keeps the type of
// the value it replaces; new values become numbers or booleans when they
// look like one, and strings otherwise.
func patchJSON(content string, set map[string]string) (string, error) {
	root := map[string]any{}
	if strings.TrimSpace(content) != "" {
		dec := json.NewDecoder(strings.NewReader(content))
		dec.UseNumber()
		if err := dec.Decode(&root); err != nil {
			return "", fmt.Errorf("not a JSON object: %w", err)
		}
	}
	for _, k := range sortedSet(set) {
		parts := strings.Split(k, ".")
		obj := root
		for _, p := range parts[:len(parts)-1] {
			next, ok := obj[p].(map[string]any)
			if !ok {
				next = map[string]any{}
				obj[p] = next
			}
			obj = next
		}
		last := parts[len(parts)-1]
		obj[last] = jsonValue(obj[last], set[k])
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	return b.String(), nil
}

func jsonValue(old any, v string) any {
	switch old.(type) {
	case string:
		return v
	case json.Number, float64:
		if _, err := strconv.ParseFloat(v, 64); err == nil {
			return json.Number(v)
		}
		return v
	case bool:
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
		return v
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	if _, err := strconv.ParseFloat(v, 64); err == nil && v != "" {
		return json.Number(v)
	}
	return v
}
