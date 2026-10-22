package tools_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/grez-lucas/sp-quadrant-mcp/internal/quadrant"
	"github.com/grez-lucas/sp-quadrant-mcp/internal/sp"
	"github.com/grez-lucas/sp-quadrant-mcp/internal/sptest"
	"github.com/grez-lucas/sp-quadrant-mcp/internal/tools"
)

const token = "toolsTokenCCCCCCCCCCCCCCCCCCCCCC"

// connect wires a real MCP client to a server with the tools registered over
// a real sp.Client talking to fake, authenticating with clientToken.
func connect(t *testing.T, fake *sptest.Fake, clientToken string) *mcp.ClientSession {
	t.Helper()
	desktop, err := sp.New(fake.URL(), clientToken)
	if err != nil {
		t.Fatalf("sp.New: %v", err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "sp-quadrant-mcp", Version: "test"}, nil)
	tools.Register(server, desktop, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		_ = serverSession.Wait()
	})
	return session
}

func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: protocol error %v (tool failures must be tool results)", name, err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func wantError(t *testing.T, res *mcp.CallToolResult, fragments ...string) {
	t.Helper()
	if !res.IsError {
		t.Fatalf("IsError = false, want a tool error; content: %s", text(res))
	}
	for _, f := range fragments {
		if !strings.Contains(text(res), f) {
			t.Fatalf("error %q does not mention %q", text(res), f)
		}
	}
}

func decode[T any](t *testing.T, res *mcp.CallToolResult) (T, map[string]any) {
	t.Helper()
	var out T
	if res.IsError {
		t.Fatalf("tool failed: %s", text(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode structured content %s: %v", raw, err)
	}
	var generic map[string]any
	_ = json.Unmarshal(raw, &generic)
	return out, generic
}

func ptr[T any](v T) *T { return &v }

func writes(fake *sptest.Fake) int {
	n := 0
	for _, r := range fake.Requests() {
		if r.Method == "POST" || r.Method == "PATCH" || r.Method == "DELETE" {
			n++
		}
	}
	return n
}

var quadrants = []quadrant.Quadrant{
	{Urgent: true, Important: true},
	{Urgent: false, Important: true},
	{Urgent: true, Important: false},
	{Urgent: false, Important: false},
}

func TestToolsAreRegisteredWithTheirInputs(t *testing.T) {
	s := connect(t, sptest.New(t, token), token)
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	required := map[string][]string{}
	for _, tool := range res.Tools {
		if tool.Description == "" {
			t.Errorf("tool %s has no description", tool.Name)
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Required []string `json:"required"`
		}
		_ = json.Unmarshal(raw, &schema)
		slices.Sort(schema.Required)
		required[tool.Name] = schema.Required
	}
	want := map[string][]string{
		"list_tasks":    nil,
		"create_task":   {"important", "title", "urgent"},
		"move_task":     {"important", "task_id", "urgent"},
		"complete_task": {"task_id"},
		"add_notes":     {"task_id", "text"},
	}
	if len(required) != len(want) {
		t.Fatalf("tools = %v, want exactly %v", required, want)
	}
	for name, w := range want {
		got, ok := required[name]
		if !ok || !slices.Equal(got, w) {
			t.Errorf("%s required inputs = %v (registered %v), want %v", name, got, ok, w)
		}
	}
}

// ADR-0001 Verification: with the EM_* tags absent, a create fails with the
// "Create tags" message and SP holds no new task.
func TestCreateTaskIsRefusedWithoutTheQuadrantTags(t *testing.T) {
	for _, q := range quadrants {
		t.Run(q.Name(), func(t *testing.T) {
			fake := sptest.New(t, token)
			s := connect(t, fake, token)

			res := call(t, s, "create_task", map[string]any{"title": "Pay rent", "urgent": q.Urgent, "important": q.Important})

			wantError(t, res, `"Create tags"`, "Eisenhower board")
			if tasks := fake.Tasks(); len(tasks) != 0 {
				t.Fatalf("SP holds %d tasks after a refused create, want none: %+v", len(tasks), tasks)
			}
			if n := writes(fake); n != 0 {
				t.Fatalf("%d writes reached SP, want none", n)
			}
		})
	}
}

func TestCreateTaskIsRefusedWithOnlyOneQuadrantTag(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddTag("EM_URGENT", "urgent")
	s := connect(t, fake, token)

	res := call(t, s, "create_task", map[string]any{"title": "Pay rent", "urgent": true, "important": false})

	wantError(t, res, `"Create tags"`)
	if len(fake.Tasks()) != 0 {
		t.Fatalf("SP holds a task after a refused create")
	}
}

// ADR-0001 Verification: with the tags present, a create reads back the
// asked-for state.
func TestCreateTaskPutsTheTaskInTheAskedForQuadrant(t *testing.T) {
	for _, q := range quadrants {
		t.Run(q.Name(), func(t *testing.T) {
			fake := sptest.New(t, token)
			fake.AddQuadrantTags()
			s := connect(t, fake, token)

			res := call(t, s, "create_task", map[string]any{"title": "  Pay rent  ", "urgent": q.Urgent, "important": q.Important})

			got, _ := decode[tools.TaskView](t, res)
			want := tools.TaskView{ID: got.ID, Title: "Pay rent", Done: false, Urgent: ptr(q.Urgent), Important: ptr(q.Important), Quadrant: q.Name()}
			if !equalView(got, want) {
				t.Fatalf("result = %s, want %s", show(got), show(want))
			}
			stored, ok := fake.Task(got.ID)
			if !ok {
				t.Fatalf("SP holds no task %s", got.ID)
			}
			if stored.Title != "Pay rent" || stored.ProjectID != "INBOX_PROJECT" || !slices.Equal(stored.TagIDs, q.TagIDs()) {
				t.Fatalf("SP holds %+v, want title %q in INBOX_PROJECT with tags %v", stored, "Pay rent", q.TagIDs())
			}
		})
	}
}

// SP fills projectId and tagIds from the view open in the app
// (createNewTaskWithDefaults) unless the request sets them, so create_task
// must land in INBOX_PROJECT with exactly the quadrant tags whatever is open.
func TestCreateTaskIgnoresTheViewOpenInSP(t *testing.T) {
	views := []struct {
		name string
		kind sptest.ContextKind
		id   string
	}{
		{"project view", sptest.ProjectContext, sptest.WorkProjectID},
		{"tag view", sptest.TagContext, "errands"},
		{"today view", sptest.TagContext, "TODAY"},
	}
	for _, v := range views {
		for _, q := range quadrants {
			t.Run(v.name+"/"+q.Name(), func(t *testing.T) {
				fake := sptest.New(t, token)
				fake.AddQuadrantTags()
				fake.AddTag("errands", "errands")
				fake.SetDefaultProjectID(sptest.WorkProjectID)
				fake.SetActiveContext(v.kind, v.id)
				s := connect(t, fake, token)

				got, _ := decode[tools.TaskView](t, call(t, s, "create_task",
					map[string]any{"title": "Pay rent", "urgent": q.Urgent, "important": q.Important}))

				stored, _ := fake.Task(got.ID)
				if stored.ProjectID != "INBOX_PROJECT" {
					t.Fatalf("SP stored projectId %q, want INBOX_PROJECT", stored.ProjectID)
				}
				if !slices.Equal(stored.TagIDs, q.TagIDs()) {
					t.Fatalf("SP stored tags %v, want exactly %v", stored.TagIDs, q.TagIDs())
				}
			})
		}
	}
}

func TestCreateTaskStoresTheTitleLiterally(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	s := connect(t, fake, token)

	res := call(t, s, "create_task", map[string]any{"title": "Email #finance about 30m call", "urgent": true, "important": true})

	got, _ := decode[tools.TaskView](t, res)
	stored, _ := fake.Task(got.ID)
	if stored.Title != "Email #finance about 30m call" {
		t.Fatalf("SP stored title %q, want it literal", stored.Title)
	}
	if !slices.Equal(stored.TagIDs, []string{"EM_URGENT", "EM_IMPORTANT"}) {
		t.Fatalf("SP stored tags %v; short syntax must not add any", stored.TagIDs)
	}
}

func TestCreateTaskRejectsABlankTitle(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	s := connect(t, fake, token)

	res := call(t, s, "create_task", map[string]any{"title": " \t ", "urgent": true, "important": true})

	wantError(t, res, "title")
	if len(fake.Tasks()) != 0 || writes(fake) != 0 {
		t.Fatalf("a blank title reached SP")
	}
}

func TestCreateTaskNeedsTheInboxProject(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	fake.RemoveProject("INBOX_PROJECT")
	s := connect(t, fake, token)

	res := call(t, s, "create_task", map[string]any{"title": "Pay rent", "urgent": true, "important": true})

	wantError(t, res, "INBOX_PROJECT")
	if len(fake.Tasks()) != 0 {
		t.Fatalf("SP holds a task although the inbox is missing")
	}
}

func TestCreateTaskFailsWhenSPDoesNotKeepTheQuadrant(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	fake.OnWrite(func(task *sptest.Task) { task.TagIDs = quadrant.OtherTagIDs(task.TagIDs) })
	s := connect(t, fake, token)

	res := call(t, s, "create_task", map[string]any{"title": "Pay rent", "urgent": true, "important": false})

	wantError(t, res, "Urgent, not important", "Neither")
}

func TestCreateTaskStoresTheNotesLiterally(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	s := connect(t, fake, token)
	const notes = "Ana has the PO #finance 30m\n\n- [ ] Ask for the invoice\n- [ ] File it"

	res := call(t, s, "create_task", map[string]any{"title": "Renew domain", "urgent": false, "important": true,
		"notes": "\n" + notes + "\n\n"})

	got, _ := decode[tools.TaskView](t, res)
	want := tools.TaskView{ID: got.ID, Title: "Renew domain", Notes: notes, Urgent: ptr(false), Important: ptr(true), Quadrant: "Not urgent, important"}
	if !equalView(got, want) {
		t.Fatalf("result = %s, want %s", show(got), show(want))
	}
	stored, _ := fake.Task(got.ID)
	if stored.Notes != notes || !slices.Equal(stored.TagIDs, []string{"EM_IMPORTANT"}) {
		t.Fatalf("SP holds notes %q tags %v, want %q and only EM_IMPORTANT", stored.Notes, stored.TagIDs, notes)
	}
}

func TestCreateTaskWithoutNotesLeavesThemEmpty(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	s := connect(t, fake, token)

	got, raw := decode[tools.TaskView](t, call(t, s, "create_task", map[string]any{"title": "Pay rent", "urgent": true, "important": true}))

	if stored, _ := fake.Task(got.ID); stored.Notes != "" {
		t.Fatalf("SP holds notes %q, want none", stored.Notes)
	}
	if _, ok := raw["notes"]; ok {
		t.Fatalf("result has notes although the task has none: %v", raw)
	}
}

func TestCreateTaskFailsWhenSPDoesNotKeepTheNotes(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	fake.OnWrite(func(task *sptest.Task) { task.Notes = "" })
	s := connect(t, fake, token)

	res := call(t, s, "create_task", map[string]any{"title": "Pay rent", "urgent": true, "important": true, "notes": "- [ ] Transfer"})

	wantError(t, res, "notes", "check it in SP")
}

func TestAddNotesAppendsToTheTasksNotes(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		text     string
		want     string
	}{
		{"to a task without notes", "", "Ana has the PO number", "Ana has the PO number"},
		{"after a paragraph", "Context: the renewal", "- [ ] Ask for the invoice", "Context: the renewal\n\n- [ ] Ask for the invoice"},
		{"continuing a checklist", "Context\n\n- [ ] One\n", "- [ ] Two", "Context\n\n- [ ] One\n- [ ] Two"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := sptest.New(t, token)
			fake.AddQuadrantTags()
			id := fake.AddTask(sptest.Task{Title: "Renew domain", Notes: c.existing, TagIDs: []string{"EM_URGENT", "work"}})
			s := connect(t, fake, token)

			res := call(t, s, "add_notes", map[string]any{"task_id": id, "text": c.text})

			got, _ := decode[tools.TaskView](t, res)
			want := tools.TaskView{ID: id, Title: "Renew domain", Notes: c.want, Urgent: ptr(true), Important: ptr(false), Quadrant: "Urgent, not important"}
			if !equalView(got, want) {
				t.Fatalf("result = %s, want %s", show(got), show(want))
			}
			stored, _ := fake.Task(id)
			if stored.Notes != c.want || stored.Title != "Renew domain" || stored.IsDone ||
				!slices.Equal(stored.TagIDs, []string{"EM_URGENT", "work"}) {
				t.Fatalf("SP holds %+v, want notes %q and everything else unchanged", stored, c.want)
			}
		})
	}
}

func TestListTasksShowsTheNotes(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	id := fake.AddTask(sptest.Task{Title: "Renew domain", Notes: "- [ ] Pay"})
	s := connect(t, fake, token)

	got, _ := decode[tools.ListTasksOutput](t, call(t, s, "list_tasks", map[string]any{}))

	if len(got.Tasks) != 1 || got.Tasks[0].ID != id || got.Tasks[0].Notes != "- [ ] Pay" {
		t.Fatalf("list_tasks = %s, want task %s with its notes", show(got), id)
	}
}

func TestAddNotesWorksWithoutTheQuadrantTags(t *testing.T) {
	fake := sptest.New(t, token)
	id := fake.AddTask(sptest.Task{Title: "Renew domain"})
	s := connect(t, fake, token)

	got, raw := decode[tools.TaskView](t, call(t, s, "add_notes", map[string]any{"task_id": id, "text": "- [ ] Pay"}))

	if got.Notes != "- [ ] Pay" {
		t.Fatalf("result notes = %q, want %q", got.Notes, "- [ ] Pay")
	}
	for _, k := range []string{"urgent", "important", "quadrant"} {
		if _, ok := raw[k]; ok {
			t.Fatalf("result has %q although the quadrant tags do not exist: %v", k, raw)
		}
	}
}

func TestAddNotesRejectsBlankText(t *testing.T) {
	fake := sptest.New(t, token)
	id := fake.AddTask(sptest.Task{Title: "Renew domain", Notes: "keep"})
	s := connect(t, fake, token)

	res := call(t, s, "add_notes", map[string]any{"task_id": id, "text": " \n\t "})

	wantError(t, res, "text")
	if stored, _ := fake.Task(id); stored.Notes != "keep" || writes(fake) != 0 {
		t.Fatalf("blank text reached SP")
	}
}

func TestAddNotesRefusesASubtask(t *testing.T) {
	fake := sptest.New(t, token)
	parent := fake.AddTask(sptest.Task{Title: "Trip"})
	child := fake.AddTask(sptest.Task{Title: "Book train", ParentID: parent})
	s := connect(t, fake, token)

	res := call(t, s, "add_notes", map[string]any{"task_id": child, "text": "- [ ] Window seat"})

	wantError(t, res, "not a Task: subtasks never appear on the board")
	if writes(fake) != 0 {
		t.Fatalf("a refused add_notes wrote to SP")
	}
}

func TestAddNotesFailsWhenSPDoesNotKeepTheNotes(t *testing.T) {
	fake := sptest.New(t, token)
	id := fake.AddTask(sptest.Task{Title: "Renew domain", Notes: "old"})
	fake.OnWrite(func(task *sptest.Task) { task.Notes = "old" })
	s := connect(t, fake, token)

	res := call(t, s, "add_notes", map[string]any{"task_id": id, "text": "- [ ] Pay"})

	wantError(t, res, "notes", "check it in SP")
}

// ADR-0001 Verification: a move reads back the asked-for state and preserves
// the task's other tags.
func TestMoveTaskChangesOnlyTheQuadrantTags(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	fake.AddTag("work", "work")
	fake.AddTag("home", "home")
	id := fake.AddTask(sptest.Task{Title: "Plan offsite", TagIDs: []string{"EM_URGENT", "work", "home"}})
	s := connect(t, fake, token)

	res := call(t, s, "move_task", map[string]any{"task_id": id, "urgent": false, "important": true})

	got, _ := decode[tools.TaskView](t, res)
	want := tools.TaskView{ID: id, Title: "Plan offsite", Urgent: ptr(false), Important: ptr(true), Quadrant: "Not urgent, important"}
	if !equalView(got, want) {
		t.Fatalf("result = %s, want %s", show(got), show(want))
	}
	stored, _ := fake.Task(id)
	if want := []string{"work", "home", "EM_IMPORTANT"}; !slices.Equal(stored.TagIDs, want) {
		t.Fatalf("SP holds tags %v, want %v", stored.TagIDs, want)
	}
}

func TestMoveTaskToNeitherClearsBothQuadrantTags(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	id := fake.AddTask(sptest.Task{Title: "Read paper", TagIDs: []string{"EM_IMPORTANT", "EM_URGENT"}})
	s := connect(t, fake, token)

	res := call(t, s, "move_task", map[string]any{"task_id": id, "urgent": false, "important": false})

	got, _ := decode[tools.TaskView](t, res)
	if got.Quadrant != "Neither" {
		t.Fatalf("quadrant = %q, want Neither", got.Quadrant)
	}
	stored, _ := fake.Task(id)
	if len(stored.TagIDs) != 0 {
		t.Fatalf("SP holds tags %v, want none", stored.TagIDs)
	}
}

func TestMoveTaskIsRefusedWithoutTheQuadrantTags(t *testing.T) {
	fake := sptest.New(t, token)
	id := fake.AddTask(sptest.Task{Title: "Plan offsite", TagIDs: []string{"work"}})
	s := connect(t, fake, token)

	res := call(t, s, "move_task", map[string]any{"task_id": id, "urgent": true, "important": true})

	wantError(t, res, `"Create tags"`)
	stored, _ := fake.Task(id)
	if !slices.Equal(stored.TagIDs, []string{"work"}) || writes(fake) != 0 {
		t.Fatalf("a refused move changed SP: tags %v", stored.TagIDs)
	}
}

func TestMoveTaskRefusesASubtask(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	parent := fake.AddTask(sptest.Task{Title: "Trip"})
	child := fake.AddTask(sptest.Task{Title: "Book train", ParentID: parent})
	s := connect(t, fake, token)

	res := call(t, s, "move_task", map[string]any{"task_id": child, "urgent": true, "important": true})

	wantError(t, res, "not a Task: subtasks never appear on the board")
	if writes(fake) != 0 {
		t.Fatalf("a refused move wrote to SP")
	}
}

func TestMoveTaskFailsWhenSPDropsAnotherTag(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	id := fake.AddTask(sptest.Task{Title: "Plan offsite", TagIDs: []string{"work", "home"}})
	fake.OnWrite(func(task *sptest.Task) {
		task.TagIDs = slices.DeleteFunc(task.TagIDs, func(id string) bool { return id == "home" })
	})
	s := connect(t, fake, token)

	res := call(t, s, "move_task", map[string]any{"task_id": id, "urgent": true, "important": true})

	wantError(t, res, "home")
}

func TestMoveTaskFailsWhenSPDoesNotKeepTheQuadrant(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	id := fake.AddTask(sptest.Task{Title: "Plan offsite", TagIDs: []string{"work"}})
	fake.OnWrite(func(task *sptest.Task) { task.TagIDs = quadrant.OtherTagIDs(task.TagIDs) })
	s := connect(t, fake, token)

	res := call(t, s, "move_task", map[string]any{"task_id": id, "urgent": true, "important": true})

	wantError(t, res, "Urgent and important", "Neither")
}

func TestMoveTaskSurfacesSPsErrorCode(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	s := connect(t, fake, token)

	res := call(t, s, "move_task", map[string]any{"task_id": "missing", "urgent": true, "important": true})

	wantError(t, res, "TASK_NOT_FOUND", "Task not found")
}

// ADR-0001 Verification: a complete reads back the asked-for state.
func TestCompleteTaskMarksItDoneInTheSameQuadrant(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	id := fake.AddTask(sptest.Task{Title: "File taxes", TagIDs: []string{"EM_URGENT", "work"}})
	s := connect(t, fake, token)

	res := call(t, s, "complete_task", map[string]any{"task_id": id})

	got, _ := decode[tools.TaskView](t, res)
	want := tools.TaskView{ID: id, Title: "File taxes", Done: true, Urgent: ptr(true), Important: ptr(false), Quadrant: "Urgent, not important"}
	if !equalView(got, want) {
		t.Fatalf("result = %s, want %s", show(got), show(want))
	}
	stored, _ := fake.Task(id)
	if !stored.IsDone || !slices.Equal(stored.TagIDs, []string{"EM_URGENT", "work"}) {
		t.Fatalf("SP holds %+v, want done with tags unchanged", stored)
	}
}

func TestCompleteTaskWorksWithoutTheQuadrantTags(t *testing.T) {
	fake := sptest.New(t, token)
	id := fake.AddTask(sptest.Task{Title: "File taxes", TagIDs: []string{"work"}})
	s := connect(t, fake, token)

	res := call(t, s, "complete_task", map[string]any{"task_id": id})

	got, raw := decode[tools.TaskView](t, res)
	if !got.Done || got.ID != id {
		t.Fatalf("result = %s, want task %s done", show(got), id)
	}
	for _, k := range []string{"urgent", "important", "quadrant"} {
		if _, ok := raw[k]; ok {
			t.Fatalf("result has %q although the quadrant tags do not exist: %v", k, raw)
		}
	}
	stored, _ := fake.Task(id)
	if !stored.IsDone {
		t.Fatalf("SP does not hold the task as done")
	}
}

func TestCompleteTaskRefusesASubtask(t *testing.T) {
	fake := sptest.New(t, token)
	parent := fake.AddTask(sptest.Task{Title: "Trip"})
	child := fake.AddTask(sptest.Task{Title: "Book train", ParentID: parent})
	s := connect(t, fake, token)

	res := call(t, s, "complete_task", map[string]any{"task_id": child})

	wantError(t, res, "not a Task: subtasks never appear on the board")
	if stored, _ := fake.Task(child); stored.IsDone || writes(fake) != 0 {
		t.Fatalf("a refused complete wrote to SP")
	}
}

func TestCompleteTaskFailsWhenSPDoesNotStoreDone(t *testing.T) {
	fake := sptest.New(t, token)
	id := fake.AddTask(sptest.Task{Title: "File taxes"})
	fake.OnWrite(func(task *sptest.Task) { task.IsDone = false })
	s := connect(t, fake, token)

	res := call(t, s, "complete_task", map[string]any{"task_id": id})

	wantError(t, res, "done")
}

func TestCompleteTaskFailsWhenTheQuadrantChanges(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	id := fake.AddTask(sptest.Task{Title: "File taxes", TagIDs: []string{"EM_IMPORTANT"}})
	fake.OnWrite(func(task *sptest.Task) { task.TagIDs = []string{} })
	s := connect(t, fake, token)

	res := call(t, s, "complete_task", map[string]any{"task_id": id})

	wantError(t, res, "Not urgent, important", "Neither")
}

func TestListTasksShowsTopLevelTasksWithTheirQuadrant(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	a := fake.AddTask(sptest.Task{Title: "Ship release", TagIDs: []string{"EM_URGENT", "EM_IMPORTANT"}})
	b := fake.AddTask(sptest.Task{Title: "Water plants", TagIDs: []string{"home"}})
	fake.AddTask(sptest.Task{Title: "Tag release", ParentID: a})
	d := fake.AddTask(sptest.Task{Title: "Ship blog post", IsDone: true, TagIDs: []string{"EM_IMPORTANT"}})
	s := connect(t, fake, token)

	got, raw := decode[tools.ListTasksOutput](t, call(t, s, "list_tasks", map[string]any{}))
	want := []tools.TaskView{
		{ID: a, Title: "Ship release", Urgent: ptr(true), Important: ptr(true), Quadrant: "Urgent and important"},
		{ID: b, Title: "Water plants", Urgent: ptr(false), Important: ptr(false), Quadrant: "Neither"},
	}
	if !equalViews(got.Tasks, want) {
		t.Fatalf("tasks = %s, want %s", show(got.Tasks), show(want))
	}
	if _, ok := raw["note"]; ok {
		t.Fatalf("unexpected note with the quadrant tags present: %v", raw["note"])
	}

	got, _ = decode[tools.ListTasksOutput](t, call(t, s, "list_tasks", map[string]any{"query": "ship", "include_done": true}))
	want = []tools.TaskView{
		{ID: a, Title: "Ship release", Urgent: ptr(true), Important: ptr(true), Quadrant: "Urgent and important"},
		{ID: d, Title: "Ship blog post", Done: true, Urgent: ptr(false), Important: ptr(true), Quadrant: "Not urgent, important"},
	}
	if !equalViews(got.Tasks, want) {
		t.Fatalf("tasks = %s, want %s", show(got.Tasks), show(want))
	}
}

func TestListTasksWithoutTheQuadrantTagsOmitsQuadrantsAndSaysWhy(t *testing.T) {
	fake := sptest.New(t, token)
	a := fake.AddTask(sptest.Task{Title: "Ship release"})
	fake.AddTask(sptest.Task{Title: "Tag release", ParentID: a})
	s := connect(t, fake, token)

	got, raw := decode[tools.ListTasksOutput](t, call(t, s, "list_tasks", nil))

	if len(got.Tasks) != 1 || got.Tasks[0].ID != a || got.Tasks[0].Title != "Ship release" {
		t.Fatalf("tasks = %s, want only %s", show(got.Tasks), a)
	}
	tasks, _ := raw["tasks"].([]any)
	first, _ := tasks[0].(map[string]any)
	for _, k := range []string{"urgent", "important", "quadrant"} {
		if _, ok := first[k]; ok {
			t.Fatalf("task has %q although the quadrant tags do not exist: %v", k, first)
		}
	}
	if !strings.Contains(got.Note, `"Create tags"`) {
		t.Fatalf("note = %q, want it to say to press \"Create tags\"", got.Note)
	}
}

func TestListTasksWithNoTasksIsAnEmptyList(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	s := connect(t, fake, token)

	_, raw := decode[tools.ListTasksOutput](t, call(t, s, "list_tasks", nil))
	if tasks, ok := raw["tasks"].([]any); !ok || len(tasks) != 0 {
		t.Fatalf("tasks = %#v, want []", raw["tasks"])
	}
}

// every tool call, with arguments that would otherwise succeed.
var everyCall = []struct {
	tool string
	args map[string]any
}{
	{"list_tasks", map[string]any{}},
	{"create_task", map[string]any{"title": "Pay rent", "urgent": true, "important": true}},
	{"move_task", map[string]any{"task_id": "task-1", "urgent": true, "important": false}},
	{"complete_task", map[string]any{"task_id": "task-1"}},
	{"add_notes", map[string]any{"task_id": "task-1", "text": "- [ ] Pay"}},
}

func seeded(t *testing.T) *sptest.Fake {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	if id := fake.AddTask(sptest.Task{Title: "Existing"}); id != "task-1" {
		t.Fatalf("seed id = %s, want task-1", id)
	}
	return fake
}

func TestEveryToolSucceedsAgainstAHealthySP(t *testing.T) {
	for _, c := range everyCall {
		t.Run(c.tool, func(t *testing.T) {
			res := call(t, connect(t, seeded(t), token), c.tool, c.args)
			if res.IsError {
				t.Fatalf("%s failed: %s", c.tool, text(res))
			}
		})
	}
}

// ADR-0001 Verification: with SP closed, a call fails with the "not running
// or Local REST API off" message.
func TestEveryToolSaysSPIsNotRunningWhenNothingListens(t *testing.T) {
	for _, c := range everyCall {
		t.Run(c.tool, func(t *testing.T) {
			fake := seeded(t)
			s := connect(t, fake, token)
			fake.Close()

			res := call(t, s, c.tool, c.args)

			wantError(t, res, "SP desktop is not running, or its Local REST API is off (Settings -> Misc -> Enable local REST API)")
		})
	}
}

// SP answers 503 API_DISABLED to everything, /health included, on a
// keep-alive connection that outlived the switch being turned off.
func TestEveryToolSaysTheAPIIsOffWhenSPAnswersAPIDisabled(t *testing.T) {
	for _, c := range everyCall {
		t.Run(c.tool, func(t *testing.T) {
			fake := seeded(t)
			fake.SetAPIEnabled(false)
			s := connect(t, fake, token)

			res := call(t, s, c.tool, c.args)

			wantError(t, res, "SP desktop is not running, or its Local REST API is off (Settings -> Misc -> Enable local REST API)")
			if len(fake.Tasks()) != 1 || writes(fake) != 0 {
				t.Fatalf("a call with the API off changed SP")
			}
		})
	}
}

func TestEveryToolSaysSPIsStartingWhileTheRendererIsNotReady(t *testing.T) {
	for _, c := range everyCall {
		t.Run(c.tool, func(t *testing.T) {
			fake := seeded(t)
			fake.SetRendererReady(false)
			s := connect(t, fake, token)

			res := call(t, s, c.tool, c.args)

			wantError(t, res, "SP desktop is still starting; retry in a moment")
			if len(fake.Tasks()) != 1 {
				t.Fatalf("a call while SP was starting changed its tasks")
			}
		})
	}
}

func TestEveryToolSaysTheTokenWasRejectedOn401(t *testing.T) {
	for _, c := range everyCall {
		t.Run(c.tool, func(t *testing.T) {
			fake := seeded(t)
			s := connect(t, fake, "staleTokenDDDDDDDDDDDDDDDDDDDDDD")

			res := call(t, s, c.tool, c.args)

			wantError(t, res, "SP rejected the token", "regenerated", "restart Claude Code")
			if strings.Contains(text(res), "staleToken") {
				t.Fatalf("the error shows the token: %s", text(res))
			}
			if len(fake.Tasks()) != 1 || writes(fake) != 0 {
				t.Fatalf("a rejected call changed SP")
			}
		})
	}
}

func TestEveryToolSaysToEnableTheAPIAndRestartWithoutAToken(t *testing.T) {
	for _, c := range everyCall {
		t.Run(c.tool, func(t *testing.T) {
			fake := seeded(t)
			s := connect(t, fake, "")

			res := call(t, s, c.tool, c.args)

			wantError(t, res, "Settings -> Misc -> Enable local REST API", "restart Claude Code")
			if n := len(fake.Requests()); n != 0 {
				t.Fatalf("%d requests reached SP without a token", n)
			}
		})
	}
}

const mayHaveApplied = "check SP before retrying, a change may already have been applied"

// A 5xx on a write (SP's renderer may act after the 15s RENDERER_TIMEOUT
// budget, or fail half-way) leaves the outcome unknown, and says so.
func TestA5xxOnAWriteSaysTheChangeMayHaveLanded(t *testing.T) {
	writesOf := []struct {
		tool string
		args map[string]any
	}{
		{"create_task", map[string]any{"title": "Pay rent", "urgent": true, "important": true}},
		{"move_task", map[string]any{"task_id": "task-1", "urgent": true, "important": false}},
		{"complete_task", map[string]any{"task_id": "task-1"}},
		{"add_notes", map[string]any{"task_id": "task-1", "text": "- [ ] Pay"}},
	}
	failures := []struct {
		status int
		code   string
	}{
		{504, "RENDERER_TIMEOUT"},
		{500, "INTERNAL_ERROR"},
		{502, "BAD_GATEWAY"},
	}
	for _, w := range writesOf {
		for _, f := range failures {
			t.Run(w.tool+"/"+f.code, func(t *testing.T) {
				fake := seeded(t)
				fake.FailWrites(f.status, f.code)
				s := connect(t, fake, token)

				res := call(t, s, w.tool, w.args)

				wantError(t, res, mayHaveApplied, f.code)
			})
		}
	}
}

// Once a write has been sent, a failed read-back leaves the outcome unknown:
// the change may well have landed, so "retry" would be the wrong advice.
func TestAFailedReadBackSaysTheChangeMayHaveLanded(t *testing.T) {
	for _, w := range []struct {
		tool string
		args map[string]any
	}{
		{"create_task", map[string]any{"title": "Pay rent", "urgent": true, "important": true}},
		{"move_task", map[string]any{"task_id": "task-1", "urgent": true, "important": false}},
		{"complete_task", map[string]any{"task_id": "task-1"}},
		{"add_notes", map[string]any{"task_id": "task-1", "text": "- [ ] Pay"}},
	} {
		t.Run(w.tool, func(t *testing.T) {
			fake := seeded(t)
			fake.FailReadsAfterFirstWrite(500, "INTERNAL_ERROR")
			s := connect(t, fake, token)

			res := call(t, s, w.tool, w.args)

			wantError(t, res, "check", "before retrying", "INTERNAL_ERROR")
			if strings.Contains(text(res), "retry once SP responds") {
				t.Fatalf("a failed read-back says to just retry: %s", text(res))
			}
			if writes(fake) != 1 {
				t.Fatalf("%d writes reached SP, want exactly the one being confirmed", writes(fake))
			}
		})
	}
}

func TestAFailedCreateReadBackNamesTheCreatedTask(t *testing.T) {
	fake := seeded(t)
	fake.FailReadsAfterFirstWrite(500, "INTERNAL_ERROR")
	s := connect(t, fake, token)

	res := call(t, s, "create_task", map[string]any{"title": "Pay rent", "urgent": true, "important": true})

	var createdID string
	for _, task := range fake.Tasks() {
		if task.Title == "Pay rent" {
			createdID = task.ID
		}
	}
	if createdID == "" {
		t.Fatalf("the fake holds no created task; the test proves nothing")
	}
	wantError(t, res, "SP created task "+createdID+" but did not answer when asked to confirm it",
		"a retry may create a duplicate")
}

func TestA5xxOnAReadDoesNotClaimAChange(t *testing.T) {
	fake := seeded(t)
	fake.FailReads(500, "INTERNAL_ERROR")
	s := connect(t, fake, token)

	res := call(t, s, "list_tasks", nil)

	wantError(t, res, "INTERNAL_ERROR")
	if strings.Contains(text(res), "may already have been applied") {
		t.Fatalf("a failed read claims a change may have landed: %s", text(res))
	}
}

func equalView(a, b tools.TaskView) bool {
	eq := func(x, y *bool) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	return a.ID == b.ID && a.Title == b.Title && a.Notes == b.Notes && a.Done == b.Done && a.Quadrant == b.Quadrant &&
		eq(a.Urgent, b.Urgent) && eq(a.Important, b.Important)
}

func equalViews(a, b []tools.TaskView) bool {
	return slices.EqualFunc(a, b, equalView)
}

func show(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
