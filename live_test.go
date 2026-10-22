//go:build sp_live

// The live contract suite runs against the real SP desktop on 127.0.0.1:3876
// with the real token file, to catch drift between SP's REST contract and the
// fake in internal/sptest. Run it deliberately, with SP open:
//
//	GOTOOLCHAIN=auto go test -tags sp_live -count=1 -run Live -v .
//
// It only touches tasks it creates: every one is titled "SS13 live ..." and
// deleted when the test ends, whatever the outcome. It sends EM_* tag ids only
// when GET /tags lists both, attaches no other tag except one the user named
// "ss13...", and creates no tag. It logs only its own tasks' titles and that
// tag's title, never the token.
package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/grez-lucas/sp-quadrant-mcp/internal/quadrant"
	"github.com/grez-lucas/sp-quadrant-mcp/internal/sp"
	"github.com/grez-lucas/sp-quadrant-mcp/internal/token"
	"github.com/grez-lucas/sp-quadrant-mcp/internal/tools"
)

const liveTitlePrefix = "SS13 live "

func liveClient(t *testing.T) (*sp.Client, string) {
	t.Helper()
	path, err := token.DefaultPath()
	if err != nil {
		t.Fatalf("locate token file: %v", err)
	}
	tok, err := token.Load(path)
	if err != nil {
		t.Fatalf("load token: %v (is SP's Local REST API enabled?)", err)
	}
	c, err := sp.New(sp.DefaultBaseURL, tok)
	if err != nil {
		t.Fatalf("sp.New: %v", err)
	}
	return c, path
}

// listLiveTasks lists only this suite's tasks (title prefix), done ones included.
func listLiveTasks(ctx context.Context, c *sp.Client) ([]tools.Task, error) {
	all, err := c.ListTasks(ctx, tools.ListFilter{Query: liveTitlePrefix, IncludeDone: true})
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(task tools.Task) bool { return !strings.HasPrefix(task.Title, liveTitlePrefix) }), nil
}

func liveTasks(t *testing.T, c *sp.Client) []tools.Task {
	t.Helper()
	tasks, err := listLiveTasks(context.Background(), c)
	if err != nil {
		t.Fatalf("list SS13 tasks: %v", err)
	}
	return tasks
}

// sweep deletes, when the test ends and whatever its outcome, every
// "SS13 live" task that did not exist when the sweep started, plus every id
// tracked explicitly (in case listing fails at cleanup time). Tasks that were
// already there, for example left by an earlier interrupted run, are left alone.
type sweep struct {
	mu      sync.Mutex
	tracked []string
}

func newSweep(t *testing.T, c *sp.Client) *sweep {
	t.Helper()
	before := liveTasks(t, c)
	if len(before) > 0 {
		t.Logf("%d SS13 live task(s) already exist; they are left alone", len(before))
	}
	s := &sweep{}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		s.mu.Lock()
		targets := slices.Clone(s.tracked)
		s.mu.Unlock()
		after, err := listLiveTasks(ctx, c)
		if err != nil {
			t.Errorf("list SS13 tasks for cleanup: %v (deleting only tracked ids)", err)
		}
		for _, task := range after {
			if !containsID(before, task.ID) && !slices.Contains(targets, task.ID) {
				targets = append(targets, task.ID)
			}
		}
		for _, id := range targets {
			if err := c.DeleteTask(ctx, id); err != nil {
				t.Errorf("clean up task %s: %v", id, err)
				continue
			}
			t.Logf("deleted SS13 live task %s", id)
		}
	})
	return s
}

func (s *sweep) track(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.Contains(s.tracked, id) {
		s.tracked = append(s.tracked, id)
	}
}

func containsID(tasks []tools.Task, id string) bool {
	return slices.ContainsFunc(tasks, func(task tools.Task) bool { return task.ID == id })
}

func liveTags(t *testing.T, c *sp.Client) []tools.Tag {
	t.Helper()
	tags, err := c.Tags(context.Background())
	if err != nil {
		t.Fatalf("GET /tags: %v", err)
	}
	return tags
}

func quadrantTagsExist(tags []tools.Tag) bool {
	ids := make([]string, 0, len(tags))
	for _, tag := range tags {
		ids = append(ids, tag.ID)
	}
	return quadrant.Exist(ids)
}

// ss13Tag is a tag the user created for this suite: its title starts with
// "ss13" (any case) and it is neither a quadrant tag nor TODAY.
func ss13Tag(tags []tools.Tag) (tools.Tag, bool) {
	for _, tag := range tags {
		if tag.ID == quadrant.UrgentTagID || tag.ID == quadrant.ImportantTagID || tag.ID == "TODAY" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(tag.Title), "ss13") {
			return tag, true
		}
	}
	return tools.Tag{}, false
}

func TestLiveRESTContract(t *testing.T) {
	c, _ := liveClient(t)
	ctx := context.Background()

	h, err := c.Health(ctx)
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	if !h.RendererReady {
		t.Fatalf("SP renderer is not ready; retry once SP has started")
	}
	t.Logf("quadrant tags (EM_URGENT and EM_IMPORTANT) present: %v", quadrantTagsExist(liveTags(t, c)))
	sw := newSweep(t, c)

	// "30m" is short syntax for a time estimate: it can change the task but
	// cannot create an entity, so a regression leaks nothing that outlives the
	// task.
	const title = liveTitlePrefix + "contract 30m"
	created, err := c.CreateTask(ctx, tools.NewTask{Title: title, ProjectID: tools.InboxProjectID, TagIDs: []string{}})
	sw.track(created.ID)
	if err != nil {
		t.Fatalf("POST /tasks: %v", err)
	}
	t.Logf("created %q (%s)", created.Title, created.ID)
	if created.Title != title || len(created.TagIDs) != 0 || created.ProjectID != tools.InboxProjectID {
		t.Fatalf("POST /tasks returned title %q tags %v project %q; want the literal title, no tags, %s",
			created.Title, created.TagIDs, created.ProjectID, tools.InboxProjectID)
	}

	// SP's ShortSyntaxEffects run after the add action, so read again later.
	time.Sleep(2 * time.Second)
	fields, err := c.TaskFields(ctx, created.ID)
	if err != nil {
		t.Fatalf("GET /tasks/:id: %v", err)
	}
	var later struct {
		Title        string   `json:"title"`
		TimeEstimate float64  `json:"timeEstimate"`
		TagIDs       []string `json:"tagIds"`
	}
	raw, _ := json.Marshal(fields)
	if err := json.Unmarshal(raw, &later); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if later.Title != title || later.TimeEstimate != 0 || len(later.TagIDs) != 0 {
		t.Fatalf("2s after create SP holds title %q timeEstimate %v tags %v; want the literal title, 0, none "+
			"(isIgnoreShortSyntax was not honoured)", later.Title, later.TimeEstimate, later.TagIDs)
	}

	got, err := c.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("GET /tasks/:id: %v", err)
	}
	if got.Title != title || got.Done || got.IsSubtask() {
		t.Fatalf("GET /tasks/:id = title %q done %v parent %q", got.Title, got.Done, got.ParentID)
	}

	open, err := c.ListTasks(ctx, tools.ListFilter{Query: liveTitlePrefix})
	if err != nil {
		t.Fatalf("GET /tasks: %v", err)
	}
	if !containsID(open, created.ID) {
		t.Fatalf("GET /tasks does not list %s", created.ID)
	}

	done := true
	if _, err := c.UpdateTask(ctx, created.ID, tools.TaskChanges{Done: &done}); err != nil {
		t.Fatalf("PATCH /tasks/:id isDone: %v", err)
	}
	open, err = c.ListTasks(ctx, tools.ListFilter{Query: liveTitlePrefix})
	if err != nil {
		t.Fatalf("GET /tasks: %v", err)
	}
	if containsID(open, created.ID) {
		t.Fatalf("GET /tasks lists %s after it was completed; done tasks must be excluded by default", created.ID)
	}
	withDone, err := c.ListTasks(ctx, tools.ListFilter{Query: liveTitlePrefix, IncludeDone: true})
	if err != nil {
		t.Fatalf("GET /tasks includeDone: %v", err)
	}
	if !containsID(withDone, created.ID) {
		t.Fatalf("GET /tasks?includeDone=true does not list %s", created.ID)
	}
}

func liveCall(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (tools.TaskView, string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	var msg []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			msg = append(msg, tc.Text)
		}
	}
	var v tools.TaskView
	if !res.IsError {
		raw, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("decode %s result: %v", name, err)
		}
	}
	return v, strings.Join(msg, "\n"), res.IsError
}

func storedTags(t *testing.T, c *sp.Client, id string) []string {
	t.Helper()
	task, err := c.GetTask(context.Background(), id)
	if err != nil {
		t.Fatalf("GET /tasks/%s: %v", id, err)
	}
	return task.TagIDs
}

func TestLiveTools(t *testing.T) {
	c, path := liveClient(t)
	tags := liveTags(t, c)
	exist := quadrantTagsExist(tags)
	t.Logf("quadrant tags (EM_URGENT and EM_IMPORTANT) present: %v", exist)
	sw := newSweep(t, c)
	session, _ := start(t, path, sp.DefaultBaseURL)

	if !exist {
		before := liveTasks(t, c)
		_, msg, isError := liveCall(t, session, "create_task",
			map[string]any{"title": liveTitlePrefix + "tool refused", "urgent": true, "important": true})
		for _, task := range liveTasks(t, c) {
			if !containsID(before, task.ID) {
				t.Errorf("create_task without the quadrant tags left task %q (%s) in SP", task.Title, task.ID)
			}
		}
		if !isError || !strings.Contains(msg, `"Create tags"`) {
			t.Fatalf("create_task without the quadrant tags: isError=%v msg=%q, want the \"Create tags\" refusal", isError, msg)
		}
		return
	}

	const title = liveTitlePrefix + "tool create"
	const notes = "SS13 live context #tag 30m\n\n- [ ] first step\n- [ ] second step"
	created, msg, isError := liveCall(t, session, "create_task",
		map[string]any{"title": title, "urgent": true, "important": false, "notes": notes})
	sw.track(created.ID)
	if isError {
		t.Fatalf("create_task: %s", msg)
	}
	t.Logf("created %q (%s) in %q", created.Title, created.ID, created.Quadrant)
	if created.Title != title || created.Quadrant != "Urgent, not important" || created.Notes != notes {
		t.Fatalf("create_task returned %+v", created)
	}

	const wantNotes = notes + "\n- [ ] third step"
	noted, msg, isError := liveCall(t, session, "add_notes", map[string]any{"task_id": created.ID, "text": "- [ ] third step"})
	if isError {
		t.Fatalf("add_notes: %s", msg)
	}
	if noted.Notes != wantNotes || noted.Quadrant != "Urgent, not important" {
		t.Fatalf("add_notes returned notes %q quadrant %q, want %q in %q", noted.Notes, noted.Quadrant, wantNotes, "Urgent, not important")
	}
	// SP's short syntax effects run after the update action, so read again later.
	time.Sleep(2 * time.Second)
	if got, err := c.GetTask(context.Background(), created.ID); err != nil || got.Notes != wantNotes || got.Title != title {
		t.Fatalf("2s after add_notes SP holds title %q notes %q (err %v), want %q and %q", got.Title, got.Notes, err, title, wantNotes)
	}

	others := []string{}
	if tag, ok := ss13Tag(tags); ok {
		t.Logf("other-tag branch ran: attaching the user's tag %q (%s) before the move", tag.Title, tag.ID)
		if _, err := c.UpdateTask(context.Background(), created.ID,
			tools.TaskChanges{TagIDs: []string{quadrant.UrgentTagID, tag.ID}}); err != nil {
			t.Fatalf("attach tag %s: %v", tag.ID, err)
		}
		others = []string{tag.ID}
	} else {
		t.Logf("other-tag branch skipped: GET /tags lists no tag titled \"ss13...\" besides EM_* and TODAY")
	}
	want := append(slices.Clone(others), quadrant.ImportantTagID)

	moved, msg, isError := liveCall(t, session, "move_task", map[string]any{"task_id": created.ID, "urgent": false, "important": true})
	if isError {
		t.Fatalf("move_task: %s", msg)
	}
	if moved.Quadrant != "Not urgent, important" {
		t.Fatalf("move_task returned quadrant %q", moved.Quadrant)
	}
	if got := storedTags(t, c, created.ID); !slices.Equal(got, want) {
		t.Fatalf("after move SP holds tags %v, want %v", got, want)
	}

	completed, msg, isError := liveCall(t, session, "complete_task", map[string]any{"task_id": created.ID})
	if isError {
		t.Fatalf("complete_task: %s", msg)
	}
	if !completed.Done || completed.Quadrant != "Not urgent, important" {
		t.Fatalf("complete_task returned done=%v quadrant=%q", completed.Done, completed.Quadrant)
	}
	if got := storedTags(t, c, created.ID); !slices.Equal(got, want) {
		t.Fatalf("after complete SP holds tags %v, want %v", got, want)
	}
}
