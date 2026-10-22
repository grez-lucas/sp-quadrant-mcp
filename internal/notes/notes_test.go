package notes_test

import (
	"testing"

	"github.com/grez-lucas/sp-quadrant-mcp/internal/notes"
)

func TestAppend(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		text     string
		want     string
	}{
		{"to empty notes", "", "Call Ana before Friday", "Call Ana before Friday"},
		{"to blank notes", " \n\t\n", "Call Ana", "Call Ana"},
		{"a paragraph after a paragraph", "Context: the renewal", "Ask for the invoice", "Context: the renewal\n\nAsk for the invoice"},
		{"a checklist after a paragraph", "Context: the renewal", "- [ ] Ask for the invoice", "Context: the renewal\n\n- [ ] Ask for the invoice"},
		{"a paragraph after a checklist", "- [ ] Ask for the invoice", "Ana has the PO number", "- [ ] Ask for the invoice\n\nAna has the PO number"},
		{"a checklist item continues a checklist", "Context\n\n- [ ] One\n- [x] Two\n", "- [ ] Three", "Context\n\n- [ ] One\n- [x] Two\n- [ ] Three"},
		{"a bullet continues a bullet list", "* one", "* two", "* one\n* two"},
		{"a numbered item continues a numbered list", "1. one", "2. two", "1. one\n2. two"},
		{"an indented item continues a list", "- one", "  - nested", "- one\n  - nested"},
		{"surrounding blank lines are dropped", "Context\n\n\n", "\n\n- [ ] One\n- [ ] Two\n\n", "Context\n\n- [ ] One\n- [ ] Two"},
		{"a dash without a space is not a list item", "- one", "-1 is not an item", "- one\n\n-1 is not an item"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := notes.Append(c.existing, c.text); got != c.want {
				t.Fatalf("Append(%q, %q) = %q, want %q", c.existing, c.text, got, c.want)
			}
		})
	}
}
