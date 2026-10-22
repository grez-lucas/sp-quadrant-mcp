package sp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/grez-lucas/sp-quadrant-mcp/internal/sp"
	"github.com/grez-lucas/sp-quadrant-mcp/internal/sptest"
	"github.com/grez-lucas/sp-quadrant-mcp/internal/tools"
)

const token = "fakeTokenAAAAAAAAAAAAAAAAAAAAAAA"

func newClient(t *testing.T, baseURL, tok string) *sp.Client {
	t.Helper()
	c, err := sp.New(baseURL, tok)
	if err != nil {
		t.Fatalf("sp.New: %v", err)
	}
	return c
}

func TestHealthReportsRendererReadinessWithoutSendingTheToken(t *testing.T) {
	fake := sptest.New(t, token)
	c := newClient(t, fake.URL(), token)

	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !h.RendererReady {
		t.Fatalf("RendererReady = false, want true")
	}

	fake.SetRendererReady(false)
	h, err = c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.RendererReady {
		t.Fatalf("RendererReady = true, want false")
	}

	for _, r := range fake.Requests() {
		if r.Path == "/health" && r.Header.Get("Authorization") != "" {
			t.Fatalf("GET /health carried an Authorization header; it is unauthenticated and must not")
		}
	}
}

func TestAuthenticatedCallsSendTheBearerTokenAndNoOrigin(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddQuadrantTags()
	c := newClient(t, fake.URL(), token)

	tags, err := c.Tags(context.Background())
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	ids := make([]string, 0, len(tags))
	for _, tag := range tags {
		ids = append(ids, tag.ID)
	}
	if want := []string{"TODAY", "EM_URGENT", "EM_IMPORTANT"}; !slices.Equal(ids, want) {
		t.Fatalf("tag ids = %v, want %v", ids, want)
	}
	if tags[1].Title != "urgent" {
		t.Fatalf("EM_URGENT title = %q, want %q", tags[1].Title, "urgent")
	}

	last := fake.Requests()[len(fake.Requests())-1]
	if got := last.Header.Get("Authorization"); got != "Bearer "+token {
		t.Fatalf("Authorization header is not the bearer token")
	}
	if got := last.Header.Get("Origin"); got != "" {
		t.Fatalf("Origin = %q, want none", got)
	}
}

func TestProjectsListsEveryProject(t *testing.T) {
	fake := sptest.New(t, token)
	c := newClient(t, fake.URL(), token)

	projects, err := c.Projects(context.Background())
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	want := []tools.Project{{ID: "INBOX_PROJECT", Title: "Inbox"}, {ID: sptest.WorkProjectID, Title: "Work"}}
	if !slices.Equal(projects, want) {
		t.Fatalf("projects = %+v, want %+v", projects, want)
	}
}

// SP answers 403 to any Host outside ALLOWED_HOSTS (DNS-rebinding guard). The
// client must send the base URL's host unchanged.
func TestRequestsCarryTheBaseURLsHost(t *testing.T) {
	fake := sptest.New(t, token)
	c := newClient(t, fake.URL(), token)

	if _, err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if _, err := c.Tags(context.Background()); err != nil {
		t.Fatalf("Tags: %v", err)
	}
	base, err := url.Parse(fake.URL())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range fake.Requests() {
		if r.Host != base.Host {
			t.Fatalf("%s %s carried Host %q, want %q", r.Method, r.Path, r.Host, base.Host)
		}
	}
}

func TestADisallowedHostIsForbidden(t *testing.T) {
	fake := sptest.New(t, token)
	base, err := url.Parse(fake.URL())
	if err != nil {
		t.Fatal(err)
	}
	c := newClient(t, "http://localhost.:"+base.Port(), token)

	_, err = c.Tags(context.Background())
	var spErr *tools.SPError
	if !errors.As(err, &spErr) || spErr.Status != http.StatusForbidden || spErr.Code != "FORBIDDEN" {
		t.Fatalf("err = %v, want 403 FORBIDDEN", err)
	}
}

func TestWrongTokenIsAnUnauthorizedSPError(t *testing.T) {
	fake := sptest.New(t, token)
	c := newClient(t, fake.URL(), "someOtherTokenBBBBBBBBBBBBBBBBBB")

	_, err := c.Tags(context.Background())
	var spErr *tools.SPError
	if !errors.As(err, &spErr) {
		t.Fatalf("err = %v, want *tools.SPError", err)
	}
	if spErr.Status != http.StatusUnauthorized || spErr.Code != "UNAUTHORIZED" {
		t.Fatalf("got %d %s, want 401 UNAUTHORIZED", spErr.Status, spErr.Code)
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "someOtherToken") {
		t.Fatalf("error message contains a token: %q", err)
	}
}

func TestErrorEnvelopesSurfaceTheirCodeAndMessage(t *testing.T) {
	fake := sptest.New(t, token)
	c := newClient(t, fake.URL(), token)

	_, err := c.GetTask(context.Background(), "nope")
	var spErr *tools.SPError
	if !errors.As(err, &spErr) {
		t.Fatalf("err = %v, want *tools.SPError", err)
	}
	if spErr.Status != http.StatusNotFound || spErr.Code != "TASK_NOT_FOUND" || spErr.Message != "Task not found" {
		t.Fatalf("got %+v, want 404 TASK_NOT_FOUND Task not found", spErr)
	}
}

func TestAppNotReadyIsAnSPError(t *testing.T) {
	fake := sptest.New(t, token)
	fake.SetRendererReady(false)
	c := newClient(t, fake.URL(), token)

	_, err := c.Tags(context.Background())
	var spErr *tools.SPError
	if !errors.As(err, &spErr) || spErr.Code != "APP_NOT_READY" || spErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want 503 APP_NOT_READY", err)
	}
}

func TestNonEnvelopeResponseIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	}))
	t.Cleanup(srv.Close)
	c := newClient(t, srv.URL, token)

	_, err := c.Tags(context.Background())
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v, want an error naming HTTP 502", err)
	}
}

func TestClosedListenerIsUnreachable(t *testing.T) {
	fake := sptest.New(t, token)
	c := newClient(t, fake.URL(), token)
	fake.Close()

	_, err := c.Health(context.Background())
	if !errors.Is(err, tools.ErrUnreachable) {
		t.Fatalf("err = %v, want tools.ErrUnreachable", err)
	}
}

func TestWithoutATokenEveryCallIsRefusedBeforeTheNetwork(t *testing.T) {
	fake := sptest.New(t, token)
	c := newClient(t, fake.URL(), "")

	if _, err := c.Health(context.Background()); !errors.Is(err, tools.ErrNoToken) {
		t.Fatalf("Health err = %v, want tools.ErrNoToken", err)
	}
	if _, err := c.Tags(context.Background()); !errors.Is(err, tools.ErrNoToken) {
		t.Fatalf("Tags err = %v, want tools.ErrNoToken", err)
	}
	if n := len(fake.Requests()); n != 0 {
		t.Fatalf("fake received %d requests, want none", n)
	}
}

func TestCreateTaskStoresTheTitleLiterallyWithTheGivenTags(t *testing.T) {
	fake := sptest.New(t, token)
	c := newClient(t, fake.URL(), token)

	created, err := c.CreateTask(context.Background(), tools.NewTask{
		Title:     "Buy #milk today",
		ProjectID: "INBOX_PROJECT",
		TagIDs:    []string{"EM_URGENT"},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	want := tools.Task{ID: created.ID, Title: "Buy #milk today", TagIDs: []string{"EM_URGENT"}, ProjectID: "INBOX_PROJECT"}
	if !equalTask(created, want) {
		t.Fatalf("created = %+v, want %+v", created, want)
	}
	stored, ok := fake.Task(created.ID)
	if !ok || stored.Title != "Buy #milk today" || !slices.Equal(stored.TagIDs, []string{"EM_URGENT"}) {
		t.Fatalf("fake holds %+v, want the literal title and only EM_URGENT", stored)
	}
}

func TestCreateTaskSendsAnEmptyTagListRatherThanOmittingIt(t *testing.T) {
	fake := sptest.New(t, token)
	fake.AddTag("errands", "errands")
	fake.SetActiveContext(sptest.TagContext, "errands")
	c := newClient(t, fake.URL(), token)

	created, err := c.CreateTask(context.Background(), tools.NewTask{Title: "t", ProjectID: "INBOX_PROJECT"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	stored, _ := fake.Task(created.ID)
	if len(stored.TagIDs) != 0 {
		t.Fatalf("SP stored tags %v: an omitted tagIds lets SP add the open tag view's tag", stored.TagIDs)
	}
	var body map[string]json.RawMessage
	last := fake.Requests()[len(fake.Requests())-1]
	if err := json.Unmarshal(last.Body, &body); err != nil {
		t.Fatalf("decode POST body: %v", err)
	}
	if got := string(body["tagIds"]); got != "[]" {
		t.Fatalf(`POST body tagIds = %s, want []`, got)
	}
}

func TestCreateTaskStoresTheNotes(t *testing.T) {
	fake := sptest.New(t, token)
	c := newClient(t, fake.URL(), token)
	const notes = "Ana has the PO number\n\n- [ ] Ask for the invoice\n- [ ] File it"

	created, err := c.CreateTask(context.Background(), tools.NewTask{Title: "Renew domain", ProjectID: "INBOX_PROJECT", Notes: notes})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created.Notes != notes {
		t.Fatalf("created notes = %q, want %q", created.Notes, notes)
	}
	if stored, _ := fake.Task(created.ID); stored.Notes != notes {
		t.Fatalf("fake holds notes %q, want %q", stored.Notes, notes)
	}
}

func TestUpdateTaskReplacesTheNotesAndLeavesTheRestAlone(t *testing.T) {
	fake := sptest.New(t, token)
	id := fake.AddTask(sptest.Task{Title: "t", Notes: "old", TagIDs: []string{"work"}})
	c := newClient(t, fake.URL(), token)

	got, err := c.UpdateTask(context.Background(), id, tools.TaskChanges{Notes: ptr("old\n\n- [ ] new")})
	if err != nil {
		t.Fatalf("UpdateTask notes: %v", err)
	}
	want := tools.Task{ID: id, Title: "t", TagIDs: []string{"work"}, ProjectID: "INBOX_PROJECT", Notes: "old\n\n- [ ] new"}
	if !equalTask(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if stored, _ := fake.Task(id); stored.Notes != "old\n\n- [ ] new" || !slices.Equal(stored.TagIDs, []string{"work"}) {
		t.Fatalf("fake holds %+v, want the new notes and the tags unchanged", stored)
	}
}

func TestGetTaskDecodesASubtask(t *testing.T) {
	fake := sptest.New(t, token)
	parent := fake.AddTask(sptest.Task{Title: "parent", TagIDs: []string{"work"}})
	child := fake.AddTask(sptest.Task{Title: "child", ParentID: parent})
	c := newClient(t, fake.URL(), token)

	got, err := c.GetTask(context.Background(), child)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	want := tools.Task{ID: child, Title: "child", TagIDs: []string{}, ProjectID: "INBOX_PROJECT", ParentID: parent}
	if !equalTask(got, want) || !got.IsSubtask() {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestGetTaskTreatsANullParentIDAsTopLevel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"data":{"id":"a","title":"t","isDone":true,"tagIds":["x"],"projectId":"INBOX_PROJECT","parentId":null}}`))
	}))
	t.Cleanup(srv.Close)
	c := newClient(t, srv.URL, token)

	got, err := c.GetTask(context.Background(), "a")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	want := tools.Task{ID: "a", Title: "t", Done: true, TagIDs: []string{"x"}, ProjectID: "INBOX_PROJECT"}
	if !equalTask(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestUpdateTaskReplacesTagsAndSetsDone(t *testing.T) {
	fake := sptest.New(t, token)
	id := fake.AddTask(sptest.Task{Title: "t", TagIDs: []string{"work", "EM_URGENT"}})
	c := newClient(t, fake.URL(), token)
	ctx := context.Background()

	got, err := c.UpdateTask(ctx, id, tools.TaskChanges{TagIDs: []string{"EM_IMPORTANT", "work"}})
	if err != nil {
		t.Fatalf("UpdateTask tags: %v", err)
	}
	if !slices.Equal(got.TagIDs, []string{"EM_IMPORTANT", "work"}) || got.Done {
		t.Fatalf("after tag replace got %+v", got)
	}

	done := true
	got, err = c.UpdateTask(ctx, id, tools.TaskChanges{Done: &done})
	if err != nil {
		t.Fatalf("UpdateTask done: %v", err)
	}
	if !got.Done || !slices.Equal(got.TagIDs, []string{"EM_IMPORTANT", "work"}) {
		t.Fatalf("a Done-only change must leave tags alone; got %+v", got)
	}

	got, err = c.UpdateTask(ctx, id, tools.TaskChanges{TagIDs: []string{}})
	if err != nil {
		t.Fatalf("UpdateTask empty tags: %v", err)
	}
	stored, _ := fake.Task(id)
	if len(got.TagIDs) != 0 || len(stored.TagIDs) != 0 {
		t.Fatalf("an empty, non-nil TagIDs must clear the tags; got %v, fake holds %v", got.TagIDs, stored.TagIDs)
	}
}

func TestListTasksAppliesTheFilterAndKeepsSubtasks(t *testing.T) {
	fake := sptest.New(t, token)
	open := fake.AddTask(sptest.Task{Title: "Write report"})
	done := fake.AddTask(sptest.Task{Title: "write tests", IsDone: true})
	child := fake.AddTask(sptest.Task{Title: "write intro", ParentID: open})
	fake.AddTask(sptest.Task{Title: "Shopping"})
	c := newClient(t, fake.URL(), token)
	ctx := context.Background()

	got, err := c.ListTasks(ctx, tools.ListFilter{Query: "WRITE"})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if ids := taskIDs(got); !slices.Equal(ids, []string{open, child}) {
		t.Fatalf("query without done = %v, want %v", ids, []string{open, child})
	}

	got, err = c.ListTasks(ctx, tools.ListFilter{Query: "write", IncludeDone: true})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if ids := taskIDs(got); !slices.Equal(ids, []string{open, done, child}) {
		t.Fatalf("query with done = %v, want %v", ids, []string{open, done, child})
	}

	last := fake.Requests()[len(fake.Requests())-1]
	q, err := url.ParseQuery(last.Query)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	if q.Get("source") != "active" {
		t.Fatalf("source = %q, want active (never archived)", q.Get("source"))
	}
}

func TestTaskFieldsReturnsSPsWholeTask(t *testing.T) {
	fake := sptest.New(t, token)
	id := fake.AddTask(sptest.Task{Title: "Call bank 30m", TimeEstimate: 1800000})
	c := newClient(t, fake.URL(), token)

	fields, err := c.TaskFields(context.Background(), id)
	if err != nil {
		t.Fatalf("TaskFields: %v", err)
	}
	if got := string(fields["timeEstimate"]); got != "1800000" {
		t.Fatalf("timeEstimate = %s, want 1800000", got)
	}
	if got := string(fields["title"]); got != `"Call bank 30m"` {
		t.Fatalf("title = %s", got)
	}

	if _, err := c.TaskFields(context.Background(), "missing"); err == nil {
		t.Fatalf("TaskFields of an unknown id returned no error")
	}
}

func TestDeleteTaskRemovesIt(t *testing.T) {
	fake := sptest.New(t, token)
	id := fake.AddTask(sptest.Task{Title: "t"})
	c := newClient(t, fake.URL(), token)

	if err := c.DeleteTask(context.Background(), id); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if _, ok := fake.Task(id); ok {
		t.Fatalf("task %s still exists", id)
	}
}

// The client must never route the token through a proxy. Go reads the proxy
// environment once per process and caches it, so the check runs in a child
// test process whose environment points every proxy variable at a recording
// listener from the start. Go also skips the environment proxy for
// "localhost" and loopback IPs, so the child reaches the fake as "localhost."
// (which the environment proxy does not exempt). SP's Host check rejects
// "localhost.", so the fake answers 403 FORBIDDEN: what matters is that the
// fake answered, not the proxy.
func TestRequestsNeverGoThroughAProxy(t *testing.T) {
	if os.Getenv(proxyChildEnv) == "1" {
		proxyChild(t)
		return
	}
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxied.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(proxy.Close)

	cmd := exec.Command(os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), proxyChildEnv+"=1", "NO_PROXY=", "no_proxy=")
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		cmd.Env = append(cmd.Env, k+"="+proxy.URL)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child with a proxy configured failed: %v\n%s", err, out)
	}
	// Without this a renamed test would match nothing and pass vacuously.
	if !strings.Contains(string(out), "--- PASS: "+t.Name()) {
		t.Fatalf("the child did not run and pass %s:\n%s", t.Name(), out)
	}
	if n := proxied.Load(); n != 0 {
		t.Fatalf("the proxy received %d requests", n)
	}
}

const proxyChildEnv = "SP_QUADRANT_PROXY_CHILD"

func proxyChild(t *testing.T) {
	fake := sptest.New(t, token)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(fake.URL(), "http://"))
	if err != nil {
		t.Fatalf("split fake URL: %v", err)
	}
	c := newClient(t, "http://localhost.:"+port, token)

	_, err = c.Tags(context.Background())
	var spErr *tools.SPError
	if !errors.As(err, &spErr) || spErr.Code != "FORBIDDEN" {
		t.Fatalf("Tags with a proxy configured: err = %v, want the fake's own 403 FORBIDDEN", err)
	}
	reached := fake.Requests()
	if len(reached) != 1 || reached[0].Path != "/tags" || reached[0].Header.Get("Authorization") != "Bearer "+token {
		t.Fatalf("the fake did not receive the request directly: %d requests", len(reached))
	}
}

func TestNewRejectsABadBaseURL(t *testing.T) {
	if _, err := sp.New("://nope", token); err == nil {
		t.Fatalf("sp.New accepted a malformed base URL")
	}
}

func taskIDs(tasks []tools.Task) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

func equalTask(a, b tools.Task) bool {
	return a.ID == b.ID && a.Title == b.Title && a.Done == b.Done && a.ProjectID == b.ProjectID &&
		a.ParentID == b.ParentID && a.Notes == b.Notes && slices.Equal(a.TagIDs, b.TagIDs)
}

func ptr[T any](v T) *T { return &v }
