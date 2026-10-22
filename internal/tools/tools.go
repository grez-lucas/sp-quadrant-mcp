// Package tools holds the MCP tools Claude Code calls to read and change
// Super Productivity's Eisenhower board, over the Desktop port. It enforces the
// constraints of ADR-0001: no quadrant write without both EM_* tags, a Move
// sends the full tag list with only the quadrant tags changed, every write is
// read back, and every call starts with a health check that never starts SP.
// Notes ride along on create and grow through add_notes, read back the same way.
package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/grez-lucas/sp-quadrant-mcp/internal/notes"
	"github.com/grez-lucas/sp-quadrant-mcp/internal/quadrant"
)

// InboxProjectID is SP's default project, where create_task puts new Tasks.
const InboxProjectID = "INBOX_PROJECT"

// Messages for the user. Each names the cause and the fix.
const (
	msgNotRunning   = "SP desktop is not running, or its Local REST API is off (Settings -> Misc -> Enable local REST API)"
	msgStarting     = "SP desktop is still starting; retry in a moment"
	msgUnauthorized = "SP rejected the token: it may have been regenerated; restart Claude Code so sp-quadrant-mcp re-reads it"
	msgNoToken      = "sp-quadrant-mcp found no SP Local REST API token when it started. " +
		"Enable SP's Local REST API (Settings -> Misc -> Enable local REST API), then restart Claude Code so it reads the token"
	msgCreateTags = `the Eisenhower tags (EM_URGENT and EM_IMPORTANT) do not exist in this sync account yet: ` +
		`open SP's Eisenhower board and press "Create tags" once, then retry`
	msgSubtask        = "not a Task: subtasks never appear on the board"
	msgMayHaveApplied = "check SP before retrying, a change may already have been applied"
)

// TaskView is a Task as the tools return it. Urgent, Important and Quadrant
// are omitted when the sync account has no quadrant tags.
type TaskView struct {
	ID        string `json:"id" jsonschema:"SP's task id, used by move_task and complete_task."`
	Title     string `json:"title" jsonschema:"The task title."`
	Notes     string `json:"notes,omitempty" jsonschema:"The task's notes, in markdown; a checklist is \"- [ ] \" items. Omitted when the task has none."`
	Done      bool   `json:"done" jsonschema:"Whether the task is completed. A completed task stays in its quadrant until archived."`
	Urgent    *bool  `json:"urgent,omitempty" jsonschema:"Whether the task carries the EM_URGENT tag."`
	Important *bool  `json:"important,omitempty" jsonschema:"Whether the task carries the EM_IMPORTANT tag."`
	Quadrant  string `json:"quadrant,omitempty" jsonschema:"The Eisenhower board panel: \"Urgent and important\", \"Not urgent, important\", \"Urgent, not important\" or \"Neither\"."`
}

// ListTasksInput is list_tasks' input.
type ListTasksInput struct {
	Query       string `json:"query,omitempty" jsonschema:"Only list tasks whose title contains this text, case-insensitively."`
	IncludeDone bool   `json:"include_done,omitempty" jsonschema:"Also list completed tasks. Defaults to false."`
}

// ListTasksOutput is list_tasks' output.
type ListTasksOutput struct {
	Tasks []TaskView `json:"tasks" jsonschema:"Top-level tasks, in SP's order."`
	Note  string     `json:"note,omitempty" jsonschema:"Present when quadrants cannot be shown, saying how to fix it."`
}

// CreateTaskInput is create_task's input.
type CreateTaskInput struct {
	Title     string `json:"title" jsonschema:"The task title, stored literally (no short syntax). Must not be blank."`
	Urgent    bool   `json:"urgent" jsonschema:"Whether the task is urgent."`
	Important bool   `json:"important" jsonschema:"Whether the task is important."`
	Notes     string `json:"notes,omitempty" jsonschema:"Optional notes, in markdown, stored literally: context that does not fit the title, and a checklist as \"- [ ] \" items, one per line."`
}

// AddNotesInput is add_notes' input.
type AddNotesInput struct {
	TaskID string `json:"task_id" jsonschema:"The id of the task, as list_tasks returns it."`
	Text   string `json:"text" jsonschema:"Markdown to add after the task's notes: a paragraph, or \"- [ ] \" checklist items, one per line. Items that follow a list continue it. Must not be blank."`
}

// MoveTaskInput is move_task's input.
type MoveTaskInput struct {
	TaskID    string `json:"task_id" jsonschema:"The id of the task to move, as list_tasks returns it."`
	Urgent    bool   `json:"urgent" jsonschema:"Whether the task should be urgent."`
	Important bool   `json:"important" jsonschema:"Whether the task should be important."`
}

// CompleteTaskInput is complete_task's input.
type CompleteTaskInput struct {
	TaskID string `json:"task_id" jsonschema:"The id of the task to complete, as list_tasks returns it."`
}

// Register adds list_tasks, create_task, move_task, complete_task and add_notes
// to server.
func Register(server *mcp.Server, desktop Desktop, logger *slog.Logger) {
	h := &handlers{desktop: desktop}
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_tasks",
		Description: "List the Tasks on Super Productivity's Eisenhower board with their quadrant " +
			"(urgent, important, and the panel name) and notes. Subtasks and archived tasks are never listed; " +
			"completed tasks only with include_done.",
	}, logged(logger, "list_tasks", h.listTasks))
	mcp.AddTool(server, &mcp.Tool{
		Name: "create_task",
		Description: "Create a Task in Super Productivity's inbox, in the quadrant given by urgent and important " +
			"(both false is the \"Neither\" quadrant), with optional markdown notes. The title and notes are stored " +
			"literally. Refused until the Eisenhower tags exist in the sync account. Returns the task as SP stored it.",
	}, logged(logger, "create_task", h.createTask))
	mcp.AddTool(server, &mcp.Tool{
		Name: "move_task",
		Description: "Move a Task to the quadrant given by urgent and important, leaving its other tags alone. " +
			"Refused for subtasks and until the Eisenhower tags exist. Returns the task as SP stored it.",
	}, logged(logger, "move_task", h.moveTask))
	mcp.AddTool(server, &mcp.Tool{
		Name: "complete_task",
		Description: "Mark a Task done. It stays in its quadrant until archived. Refused for subtasks. " +
			"Returns the task as SP stored it.",
	}, logged(logger, "complete_task", h.completeTask))
	mcp.AddTool(server, &mcp.Tool{
		Name: "add_notes",
		Description: "Add markdown after a Task's notes, keeping what is there: context, or \"- [ ] \" checklist items. " +
			"Checklist items that follow a list continue it; anything else starts a new paragraph. " +
			"Refused for subtasks. Returns the task as SP stored it.",
	}, logged(logger, "add_notes", h.addNotes))
}

// logged adapts fn to the SDK's typed handler and logs each call's outcome.
// Returning an error makes the SDK answer with an IsError tool result.
func logged[In, Out any](logger *slog.Logger, tool string, fn func(context.Context, In) (Out, error)) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		start := time.Now()
		out, err := fn(ctx, in)
		if err != nil {
			logger.WarnContext(ctx, "tool call failed", "tool", tool, "duration", time.Since(start), "err", err)
			var zero Out
			return nil, zero, err
		}
		logger.InfoContext(ctx, "tool call", "tool", tool, "duration", time.Since(start))
		return nil, out, nil
	}
}

type handlers struct {
	desktop Desktop
}

func (h *handlers) listTasks(ctx context.Context, in ListTasksInput) (ListTasksOutput, error) {
	if err := h.ready(ctx); err != nil {
		return ListTasksOutput{}, err
	}
	exist, err := h.quadrantTagsExist(ctx)
	if err != nil {
		return ListTasksOutput{}, err
	}
	tasks, err := h.desktop.ListTasks(ctx, ListFilter(in))
	if err != nil {
		return ListTasksOutput{}, explain("list tasks", err)
	}
	out := ListTasksOutput{Tasks: make([]TaskView, 0, len(tasks))}
	for _, t := range tasks {
		if t.IsSubtask() {
			continue
		}
		out.Tasks = append(out.Tasks, view(t, exist))
	}
	if !exist {
		out.Note = "Quadrants are not shown because " + msgCreateTags + "."
	}
	return out, nil
}

func (h *handlers) createTask(ctx context.Context, in CreateTaskInput) (TaskView, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return TaskView{}, errors.New("title must not be blank")
	}
	want := quadrant.Quadrant{Urgent: in.Urgent, Important: in.Important}
	wantNotes := notes.Append("", in.Notes)
	if err := h.ready(ctx); err != nil {
		return TaskView{}, err
	}
	if err := h.requireQuadrantTags(ctx); err != nil {
		return TaskView{}, err
	}
	projects, err := h.desktop.Projects(ctx)
	if err != nil {
		return TaskView{}, explain("list projects", err)
	}
	if !slices.ContainsFunc(projects, func(p Project) bool { return p.ID == InboxProjectID }) {
		return TaskView{}, fmt.Errorf("SP has no %s project to create the task in", InboxProjectID)
	}
	created, err := h.desktop.CreateTask(ctx, NewTask{Title: title, ProjectID: InboxProjectID, TagIDs: want.TagIDs(), Notes: wantNotes})
	if err != nil {
		return TaskView{}, explainWrite("create the task", err)
	}
	got, err := h.desktop.GetTask(ctx, created.ID)
	if err != nil {
		return TaskView{}, fmt.Errorf("SP created task %s but did not answer when asked to confirm it; "+
			"check it in SP before retrying, or a retry may create a duplicate: %w", created.ID, err)
	}
	if q := quadrant.Of(got.TagIDs); q != want {
		return TaskView{}, fmt.Errorf("SP created task %s but reads it back in %q, not %q; check it on the Eisenhower board",
			got.ID, q.Name(), want.Name())
	}
	if got.Notes != wantNotes {
		return TaskView{}, fmt.Errorf("SP created task %s but reads back notes %q, not %q; check it in SP",
			got.ID, got.Notes, wantNotes)
	}
	return view(got, true), nil
}

func (h *handlers) moveTask(ctx context.Context, in MoveTaskInput) (TaskView, error) {
	if in.TaskID == "" {
		return TaskView{}, errors.New("task_id must not be empty")
	}
	want := quadrant.Quadrant{Urgent: in.Urgent, Important: in.Important}
	if err := h.ready(ctx); err != nil {
		return TaskView{}, err
	}
	if err := h.requireQuadrantTags(ctx); err != nil {
		return TaskView{}, err
	}
	before, err := h.topLevelTask(ctx, in.TaskID)
	if err != nil {
		return TaskView{}, err
	}
	if _, err := h.desktop.UpdateTask(ctx, before.ID, TaskChanges{TagIDs: quadrant.Move(before.TagIDs, want)}); err != nil {
		return TaskView{}, explainWrite(fmt.Sprintf("move task %s", before.ID), err)
	}
	got, err := h.desktop.GetTask(ctx, before.ID)
	if err != nil {
		return TaskView{}, unconfirmed("move", before.ID, err)
	}
	if q := quadrant.Of(got.TagIDs); q != want {
		return TaskView{}, fmt.Errorf("SP moved task %s but reads it back in %q, not %q; check it on the Eisenhower board",
			got.ID, q.Name(), want.Name())
	}
	if lost := missing(quadrant.OtherTagIDs(before.TagIDs), got.TagIDs); len(lost) > 0 {
		return TaskView{}, fmt.Errorf("SP dropped tag(s) %s from task %s during the move; re-add them in SP",
			strings.Join(lost, ", "), got.ID)
	}
	return view(got, true), nil
}

func (h *handlers) completeTask(ctx context.Context, in CompleteTaskInput) (TaskView, error) {
	if in.TaskID == "" {
		return TaskView{}, errors.New("task_id must not be empty")
	}
	if err := h.ready(ctx); err != nil {
		return TaskView{}, err
	}
	exist, err := h.quadrantTagsExist(ctx)
	if err != nil {
		return TaskView{}, err
	}
	before, err := h.topLevelTask(ctx, in.TaskID)
	if err != nil {
		return TaskView{}, err
	}
	done := true
	if _, err := h.desktop.UpdateTask(ctx, before.ID, TaskChanges{Done: &done}); err != nil {
		return TaskView{}, explainWrite(fmt.Sprintf("complete task %s", before.ID), err)
	}
	got, err := h.desktop.GetTask(ctx, before.ID)
	if err != nil {
		return TaskView{}, unconfirmed("completion", before.ID, err)
	}
	if !got.Done {
		return TaskView{}, fmt.Errorf("SP did not store task %s as done; check it in SP", got.ID)
	}
	if was, is := quadrant.Of(before.TagIDs), quadrant.Of(got.TagIDs); was != is {
		return TaskView{}, fmt.Errorf("task %s moved from %q to %q while being completed; check it on the Eisenhower board",
			got.ID, was.Name(), is.Name())
	}
	return view(got, exist), nil
}

func (h *handlers) addNotes(ctx context.Context, in AddNotesInput) (TaskView, error) {
	if in.TaskID == "" {
		return TaskView{}, errors.New("task_id must not be empty")
	}
	if strings.TrimSpace(in.Text) == "" {
		return TaskView{}, errors.New("text must not be blank")
	}
	if err := h.ready(ctx); err != nil {
		return TaskView{}, err
	}
	exist, err := h.quadrantTagsExist(ctx)
	if err != nil {
		return TaskView{}, err
	}
	before, err := h.topLevelTask(ctx, in.TaskID)
	if err != nil {
		return TaskView{}, err
	}
	want := notes.Append(before.Notes, in.Text)
	if _, err := h.desktop.UpdateTask(ctx, before.ID, TaskChanges{Notes: &want}); err != nil {
		return TaskView{}, explainWrite(fmt.Sprintf("add notes to task %s", before.ID), err)
	}
	got, err := h.desktop.GetTask(ctx, before.ID)
	if err != nil {
		return TaskView{}, unconfirmed("notes", before.ID, err)
	}
	if got.Notes != want {
		return TaskView{}, fmt.Errorf("SP was sent the notes of task %s but reads back %q, not %q; check it in SP",
			got.ID, got.Notes, want)
	}
	return view(got, exist), nil
}

// ready is the health check every call starts with. It never starts SP.
func (h *handlers) ready(ctx context.Context) error {
	health, err := h.desktop.Health(ctx)
	if err != nil {
		var spErr *SPError
		switch {
		case errors.Is(err, ErrUnreachable):
			return fmt.Errorf("%s: %w", msgNotRunning, err)
		case errors.As(err, &spErr) && spErr.Code == "API_DISABLED":
			return errors.New(msgNotRunning)
		}
		return explain("check SP's health", err)
	}
	if !health.RendererReady {
		return errors.New(msgStarting)
	}
	return nil
}

func (h *handlers) quadrantTagsExist(ctx context.Context) (bool, error) {
	tags, err := h.desktop.Tags(ctx)
	if err != nil {
		return false, explain("list tags", err)
	}
	ids := make([]string, 0, len(tags))
	for _, t := range tags {
		ids = append(ids, t.ID)
	}
	return quadrant.Exist(ids), nil
}

func (h *handlers) requireQuadrantTags(ctx context.Context) error {
	exist, err := h.quadrantTagsExist(ctx)
	if err != nil {
		return err
	}
	if !exist {
		return errors.New("refused: " + msgCreateTags)
	}
	return nil
}

func (h *handlers) topLevelTask(ctx context.Context, id string) (Task, error) {
	task, err := h.desktop.GetTask(ctx, id)
	if err != nil {
		return Task{}, explain(fmt.Sprintf("read task %s", id), err)
	}
	if task.IsSubtask() {
		return Task{}, fmt.Errorf("task %s is %s", id, msgSubtask)
	}
	return task, nil
}

// unconfirmed is the error for a read-back that failed after SP was sent a
// write: whatever the read-back error, the write may have landed.
func unconfirmed(change, id string, err error) error {
	return fmt.Errorf("SP was sent the %s of task %s but did not answer when asked to confirm it; %s: %w",
		change, id, msgMayHaveApplied, err)
}

// explain turns the error of a read step before any write into a message for
// the user, naming the step. Read-backs after a write use unconfirmed.
func explain(step string, err error) error { return explainStep(step, err, false) }

// explainWrite is explain for a step that asks SP to change a task. A
// transport failure or a 5xx there (RENDERER_TIMEOUT: SP's renderer may still
// apply the change after the 15s budget; INTERNAL_ERROR: it may have failed
// half-way) leaves the outcome unknown.
func explainWrite(step string, err error) error { return explainStep(step, err, true) }

func explainStep(step string, err error, write bool) error {
	var spErr *SPError
	isSP := errors.As(err, &spErr)
	switch {
	case errors.Is(err, ErrNoToken):
		return errors.New(msgNoToken)
	case isSP && spErr.Code == "UNAUTHORIZED":
		return errors.New(msgUnauthorized)
	// SP answers these two before it forwards anything, so nothing changed.
	case isSP && spErr.Code == "APP_NOT_READY":
		return errors.New(msgStarting)
	case isSP && spErr.Code == "API_DISABLED":
		return errors.New(msgNotRunning)
	case write && (errors.Is(err, ErrUnreachable) || (isSP && spErr.Status >= 500)):
		return fmt.Errorf("SP desktop failed while trying to %s; %s: %w", step, msgMayHaveApplied, err)
	case errors.Is(err, ErrUnreachable):
		return fmt.Errorf("SP desktop stopped answering while trying to %s; retry once SP responds: %w", step, err)
	}
	return fmt.Errorf("could not %s: %w", step, err)
}

func view(t Task, withQuadrant bool) TaskView {
	v := TaskView{ID: t.ID, Title: t.Title, Notes: t.Notes, Done: t.Done}
	if withQuadrant {
		q := quadrant.Of(t.TagIDs)
		v.Urgent, v.Important, v.Quadrant = &q.Urgent, &q.Important, q.Name()
	}
	return v
}

// missing is every id in want that is not in have, in want's order.
func missing(want, have []string) []string {
	var out []string
	for _, id := range want {
		if !slices.Contains(have, id) {
			out = append(out, id)
		}
	}
	return out
}
