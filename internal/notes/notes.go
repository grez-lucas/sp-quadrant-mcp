// Package notes is how a Task's Notes grow: text appended to SP's markdown
// notes field, where a checklist is "- [ ] " items. It is pure: no I/O.
package notes

import (
	"regexp"
	"strings"
)

// listItem matches a markdown list item: a bullet ("-", "*", "+") or a number
// ("1." or "1)") followed by a space, after optional indentation. A checklist
// item ("- [ ] ...") is a bullet.
var listItem = regexp.MustCompile(`^\s*([-*+]|\d+[.)])\s`)

// Append adds text after existing, dropping blank lines around both. A list
// item that follows a list item continues the same list (one newline); any
// other text starts a new paragraph (a blank line).
func Append(existing, text string) string {
	existing = strings.TrimRight(existing, " \t\r\n")
	text = trimBlankLines(text)
	if existing == "" {
		return text
	}
	if listItem.MatchString(lastLine(existing)) && listItem.MatchString(firstLine(text)) {
		return existing + "\n" + text
	}
	return existing + "\n\n" + text
}

// trimBlankLines drops leading and trailing blank lines and trailing spaces,
// keeping the first line's indentation.
func trimBlankLines(s string) string {
	s = strings.TrimRight(s, " \t\r\n")
	lead := len(s) - len(strings.TrimLeft(s, " \t\r\n"))
	if i := strings.LastIndexByte(s[:lead], '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func lastLine(s string) string {
	return s[strings.LastIndexByte(s, '\n')+1:]
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
