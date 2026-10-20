// Package quadrant is the domain of Super Productivity's Eisenhower board: which
// Quadrant a Task is in, and how a Move changes a Task's tags. It is pure: no I/O.
package quadrant

// Tag ids SP's Eisenhower board reads. SP creates the tag entities only when
// "Create tags" is pressed on the board.
const (
	UrgentTagID    = "EM_URGENT"
	ImportantTagID = "EM_IMPORTANT"
)

// Quadrant is the Eisenhower board panel a Task appears in, determined only by
// whether it is Urgent and whether it is Important.
type Quadrant struct {
	Urgent    bool
	Important bool
}

// Name is the panel's title on SP's Eisenhower board.
func (q Quadrant) Name() string {
	switch {
	case q.Urgent && q.Important:
		return "Urgent and important"
	case q.Important:
		return "Not urgent, important"
	case q.Urgent:
		return "Urgent, not important"
	default:
		return "Neither"
	}
}

// TagIDs is the quadrant tags a Task in q carries: EM_URGENT first, then
// EM_IMPORTANT. Never nil, so it encodes as a JSON list even when empty.
func (q Quadrant) TagIDs() []string {
	ids := make([]string, 0, 2)
	if q.Urgent {
		ids = append(ids, UrgentTagID)
	}
	if q.Important {
		ids = append(ids, ImportantTagID)
	}
	return ids
}

// Of derives the Quadrant of a Task from its tag ids.
func Of(tagIDs []string) Quadrant {
	var q Quadrant
	for _, id := range tagIDs {
		switch id {
		case UrgentTagID:
			q.Urgent = true
		case ImportantTagID:
			q.Important = true
		}
	}
	return q
}

// Exist reports whether both quadrant tags are among the given existing tag
// ids. A Quadrant exists only once both tags exist in the sync account.
func Exist(existingTagIDs []string) bool {
	var urgent, important bool
	for _, id := range existingTagIDs {
		switch id {
		case UrgentTagID:
			urgent = true
		case ImportantTagID:
			important = true
		}
	}
	return urgent && important
}

// OtherTagIDs is every tag id that is not a quadrant tag, in its original order.
// Never nil.
func OtherTagIDs(tagIDs []string) []string {
	others := make([]string, 0, len(tagIDs))
	for _, id := range tagIDs {
		if id != UrgentTagID && id != ImportantTagID {
			others = append(others, id)
		}
	}
	return others
}

// Move is the full tag list a Task carries after moving it to q: its other tags
// in their original order, followed by q's quadrant tags. It returns a new
// slice, never nil, because SP replaces a Task's whole tag list on every write.
func Move(tagIDs []string, to Quadrant) []string {
	return append(OtherTagIDs(tagIDs), to.TagIDs()...)
}
