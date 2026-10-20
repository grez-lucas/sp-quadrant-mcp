package quadrant_test

import (
	"slices"
	"testing"

	"github.com/grez-lucas/sp-quadrant-mcp/internal/quadrant"
)

func TestOfDerivesTheQuadrantFromTagIDs(t *testing.T) {
	tests := []struct {
		name   string
		tagIDs []string
		want   quadrant.Quadrant
	}{
		{"no tags", nil, quadrant.Quadrant{}},
		{"only other tags", []string{"work", "home"}, quadrant.Quadrant{}},
		{"urgent", []string{"EM_URGENT"}, quadrant.Quadrant{Urgent: true}},
		{"important", []string{"work", "EM_IMPORTANT"}, quadrant.Quadrant{Important: true}},
		{"both, any order", []string{"EM_IMPORTANT", "x", "EM_URGENT"}, quadrant.Quadrant{Urgent: true, Important: true}},
		{"ids are case-sensitive", []string{"em_urgent", "EM_important"}, quadrant.Quadrant{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := quadrant.Of(tt.tagIDs); got != tt.want {
				t.Fatalf("Of(%v) = %+v, want %+v", tt.tagIDs, got, tt.want)
			}
		})
	}
}

func TestNameIsTheEisenhowerPanelName(t *testing.T) {
	tests := []struct {
		q    quadrant.Quadrant
		want string
	}{
		{quadrant.Quadrant{Urgent: true, Important: true}, "Urgent and important"},
		{quadrant.Quadrant{Urgent: false, Important: true}, "Not urgent, important"},
		{quadrant.Quadrant{Urgent: true, Important: false}, "Urgent, not important"},
		{quadrant.Quadrant{}, "Neither"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.q.Name(); got != tt.want {
				t.Fatalf("%+v.Name() = %q, want %q", tt.q, got, tt.want)
			}
		})
	}
}

func TestTagIDsListsTheQuadrantTagsUrgentFirst(t *testing.T) {
	tests := []struct {
		q    quadrant.Quadrant
		want []string
	}{
		{quadrant.Quadrant{Urgent: true, Important: true}, []string{"EM_URGENT", "EM_IMPORTANT"}},
		{quadrant.Quadrant{Important: true}, []string{"EM_IMPORTANT"}},
		{quadrant.Quadrant{Urgent: true}, []string{"EM_URGENT"}},
		{quadrant.Quadrant{}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.q.Name(), func(t *testing.T) {
			got := tt.q.TagIDs()
			if got == nil || !slices.Equal(got, tt.want) {
				t.Fatalf("%+v.TagIDs() = %#v, want %#v", tt.q, got, tt.want)
			}
		})
	}
}

func TestMoveChangesOnlyTheQuadrantTags(t *testing.T) {
	tests := []struct {
		name   string
		tagIDs []string
		to     quadrant.Quadrant
		want   []string
	}{
		{"neither to both", nil, quadrant.Quadrant{Urgent: true, Important: true}, []string{"EM_URGENT", "EM_IMPORTANT"}},
		{"both to neither leaves an empty list", []string{"EM_IMPORTANT", "EM_URGENT"}, quadrant.Quadrant{}, []string{}},
		{
			"other tags keep their order, quadrant tags go last",
			[]string{"EM_URGENT", "work", "EM_IMPORTANT", "home", "errand"},
			quadrant.Quadrant{Important: true},
			[]string{"work", "home", "errand", "EM_IMPORTANT"},
		},
		{"urgent to important", []string{"a", "EM_URGENT"}, quadrant.Quadrant{Important: true}, []string{"a", "EM_IMPORTANT"}},
		{"duplicate quadrant tags collapse", []string{"EM_URGENT", "EM_URGENT", "b"}, quadrant.Quadrant{Urgent: true}, []string{"b", "EM_URGENT"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quadrant.Move(tt.tagIDs, tt.to)
			if got == nil || !slices.Equal(got, tt.want) {
				t.Fatalf("Move(%v, %+v) = %#v, want %#v", tt.tagIDs, tt.to, got, tt.want)
			}
			if again := quadrant.Move(got, tt.to); !slices.Equal(again, got) {
				t.Fatalf("Move is not idempotent: second Move gave %#v, first gave %#v", again, got)
			}
			if q := quadrant.Of(got); q != tt.to {
				t.Fatalf("Of(Move(...)) = %+v, want %+v", q, tt.to)
			}
		})
	}
}

func TestMoveDoesNotAliasItsInput(t *testing.T) {
	in := []string{"work", "EM_URGENT", "home"}
	got := quadrant.Move(in, quadrant.Quadrant{Important: true})
	got[0] = "changed"
	if !slices.Equal(in, []string{"work", "EM_URGENT", "home"}) {
		t.Fatalf("Move modified its input: %v", in)
	}
}

func TestOtherTagIDsDropsOnlyQuadrantTags(t *testing.T) {
	got := quadrant.OtherTagIDs([]string{"EM_IMPORTANT", "work", "EM_URGENT", "home"})
	if want := []string{"work", "home"}; !slices.Equal(got, want) {
		t.Fatalf("OtherTagIDs = %v, want %v", got, want)
	}
}

func TestExistIsTrueOnlyWhenBothQuadrantTagsExist(t *testing.T) {
	tests := []struct {
		name   string
		tagIDs []string
		want   bool
	}{
		{"fresh profile has only TODAY", []string{"TODAY"}, false},
		{"only urgent", []string{"TODAY", "EM_URGENT"}, false},
		{"only important", []string{"EM_IMPORTANT"}, false},
		{"both", []string{"TODAY", "EM_IMPORTANT", "KANBAN_IN_PROGRESS", "EM_URGENT"}, true},
		{"none", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := quadrant.Exist(tt.tagIDs); got != tt.want {
				t.Fatalf("Exist(%v) = %v, want %v", tt.tagIDs, got, tt.want)
			}
		})
	}
}
