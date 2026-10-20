// Package sptest is a stateful, in-process stand-in for SP desktop's Local REST
// API, modelled on Super Productivity v19.2.0 (electron/local-rest-api.ts and
// src/app/core/electron/local-rest-api-handler.service.ts). Tests use it so
// they never reach the real SP on 127.0.0.1:3876.
package sptest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Task is a task as the fake stores and serves it, in SP's JSON shape.
type Task struct {
	ID             string           `json:"id"`
	Title          string           `json:"title"`
	IsDone         bool             `json:"isDone"`
	TagIDs         []string         `json:"tagIds"`
	ProjectID      string           `json:"projectId"`
	DueDay         string           `json:"dueDay,omitempty"`
	ParentID       string           `json:"parentId,omitempty"`
	SubTaskIDs     []string         `json:"subTaskIds"`
	DoneOn         *int64           `json:"doneOn,omitempty"`
	Notes          string           `json:"notes,omitempty"`
	TimeSpent      int64            `json:"timeSpent"`
	TimeEstimate   int64            `json:"timeEstimate"`
	TimeSpentOnDay map[string]int64 `json:"timeSpentOnDay"`
	Attachments    []any            `json:"attachments"`
	Created        int64            `json:"created"`
}

type tag struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	TaskIDs []string `json:"taskIds"`
	Color   *string  `json:"color"`
	Created int64    `json:"created"`
}

type project struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	IsArchived bool     `json:"isArchived"`
	TaskIDs    []string `json:"taskIds"`
}

// Request is one request the fake received.
type Request struct {
	Method string
	Host   string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
}

// WorkProjectID is a project New adds next to the inbox. It is the view open
// in the fake SP until SetActiveContext changes it.
const WorkProjectID = "WORK_PROJECT"

// ContextKind is the kind of view open in SP (its active work context).
type ContextKind int

// The kinds of work context SP's createNewTaskWithDefaults distinguishes.
const (
	ProjectContext ContextKind = iota
	TagContext
)

type failure struct {
	status int
	code   string
}

// Fake is the stand-in SP. Its zero value is not usable; call New.
type Fake struct {
	server *httptest.Server
	token  string
	// port is set before the server starts and never written again.
	port int

	mu               sync.Mutex
	apiDisabled      bool
	rendererReady    bool
	contextKind      ContextKind
	contextID        string
	defaultProjectID string
	failWrites       *failure
	failReads        *failure
	failReadsAfter   *failure // applies once wrote is true
	wrote            bool
	tags             []tag
	projects         []project
	tasks            []Task // insertion order, like SP's entity ids
	nextID           int
	onWrite          func(*Task)
	requests         []Request
}

// New starts a fake SP that accepts the given bearer token. It holds the
// virtual TODAY tag, the INBOX_PROJECT project, a WorkProjectID project that
// is the open view, and no tasks. It stops when the test ends.
func New(t testing.TB, token string) *Fake {
	t.Helper()
	f := &Fake{
		token:         token,
		rendererReady: true,
		contextKind:   ProjectContext,
		contextID:     WorkProjectID,
		tags:          []tag{{ID: "TODAY", Title: "Today", TaskIDs: []string{}}},
		projects: []project{
			{ID: "INBOX_PROJECT", Title: "Inbox", TaskIDs: []string{}},
			{ID: WorkProjectID, Title: "Work", TaskIDs: []string{}},
		},
	}
	f.server = httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	f.port = f.server.Listener.Addr().(*net.TCPAddr).Port
	f.server.Start()
	t.Cleanup(f.server.Close)
	return f
}

// URL is the fake's base URL.
func (f *Fake) URL() string { return f.server.URL }

// Close stops the fake, as if SP were quit. Later requests are refused.
func (f *Fake) Close() { f.server.Close() }

// SetRendererReady sets what GET /health reports, and whether authenticated
// requests are served or answered 503 APP_NOT_READY.
func (f *Fake) SetRendererReady(ready bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rendererReady = ready
}

// SetAPIEnabled models the Local REST API switch. While it is off, SP answers
// every request, /health included, with 503 API_DISABLED (a keep-alive
// connection that outlived the switch; new connections are refused).
func (f *Fake) SetAPIEnabled(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiDisabled = !enabled
}

// SetActiveContext sets the view open in SP. A task created without projectId
// or tagIds takes them from it, as createNewTaskWithDefaults does.
func (f *Fake) SetActiveContext(kind ContextKind, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.contextKind, f.contextID = kind, id
}

// SetDefaultProjectID sets SP's "default project" setting
// (tasks.defaultProjectId), used for new tasks outside a project view.
func (f *Fake) SetDefaultProjectID(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.defaultProjectID = id
}

// FailWrites makes every POST and PATCH on /tasks apply its change and then
// answer with the given error, as SP does when the renderer acts but answers
// after the 15s budget (504 RENDERER_TIMEOUT).
func (f *Fake) FailWrites(status int, code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failWrites = &failure{status: status, code: code}
}

// FailReads makes every authenticated GET answer with the given error.
func (f *Fake) FailReads(status int, code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failReads = &failure{status: status, code: code}
}

// FailReadsAfterFirstWrite lets every request through until a POST or PATCH
// on /tasks has been applied, then makes every authenticated GET answer with
// the given error: SP accepted a write and failed the read that confirms it.
func (f *Fake) FailReadsAfterFirstWrite(status int, code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failReadsAfter = &failure{status: status, code: code}
}

// AddTag adds a tag entity, as "Create tags" on the Eisenhower board does.
func (f *Fake) AddTag(id, title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tags = append(f.tags, tag{ID: id, Title: title, TaskIDs: []string{}})
}

// AddQuadrantTags adds EM_URGENT and EM_IMPORTANT, as pressing "Create tags"
// on SP's Eisenhower board does.
func (f *Fake) AddQuadrantTags() {
	f.AddTag("EM_URGENT", "urgent")
	f.AddTag("EM_IMPORTANT", "important")
}

// RemoveProject removes a project by id.
func (f *Fake) RemoveProject(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.projects = slices.DeleteFunc(f.projects, func(p project) bool { return p.ID == id })
}

// AddTask stores a task directly and returns its id. A task with a ParentID is
// a subtask and is linked into its parent's SubTaskIDs.
func (f *Fake) AddTask(task Task) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if task.ID == "" {
		task.ID = f.newIDLocked()
	}
	if task.ProjectID == "" {
		task.ProjectID = "INBOX_PROJECT"
	}
	normalise(&task)
	if task.ParentID != "" {
		if parent := f.findLocked(task.ParentID); parent != nil {
			parent.SubTaskIDs = append(parent.SubTaskIDs, task.ID)
		}
	}
	f.tasks = append(f.tasks, task)
	return task.ID
}

// Task returns a copy of the stored task with the given id.
func (f *Fake) Task(id string) (Task, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if task := f.findLocked(id); task != nil {
		return clone(*task), true
	}
	return Task{}, false
}

// Tasks returns a copy of every stored task.
func (f *Fake) Tasks() []Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Task, 0, len(f.tasks))
	for _, task := range f.tasks {
		out = append(out, clone(task))
	}
	return out
}

// OnWrite installs a hook that changes a task after every POST or PATCH has
// stored it and before it is returned, to model SP storing something other
// than what was asked (for example data repair stripping tags).
func (f *Fake) OnWrite(hook func(*Task)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onWrite = hook
}

// Requests returns every request received so far.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", err.Error())
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, Request{
		Method: r.Method,
		Host:   r.Host,
		Path:   r.URL.Path,
		Query:  r.URL.RawQuery,
		Header: r.Header.Clone(),
		Body:   body,
	})

	// Same order of checks as electron/local-rest-api.ts handleHttpRequest.
	if f.apiDisabled {
		writeError(w, http.StatusServiceUnavailable, "API_DISABLED", "Local REST API is disabled")
		return
	}
	if !f.allowedHost(r.Host) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Invalid Host header")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Requests from web origins are not allowed")
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/health" {
		writeData(w, http.StatusOK, map[string]any{"server": "up", "rendererReady": f.rendererReady})
		return
	}
	if !f.authorised(r.Header.Get("Authorization")) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED",
			"Invalid authorization token. Find the token in Settings → Misc → Access Token.")
		return
	}
	if !f.rendererReady {
		writeError(w, http.StatusServiceUnavailable, "APP_NOT_READY", "Renderer is not ready yet")
		return
	}

	if f.failReads != nil && r.Method == http.MethodGet {
		writeError(w, f.failReads.status, f.failReads.code, "injected read failure")
		return
	}
	if f.failReadsAfter != nil && f.wrote && r.Method == http.MethodGet {
		writeError(w, f.failReadsAfter.status, f.failReadsAfter.code, "injected read failure after a write")
		return
	}

	var parsed any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &parsed); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", err.Error())
			return
		}
	}
	f.route(w, r, body, parsed)
}

// allowedHost mirrors ALLOWED_HOSTS: SP's host and "localhost", each with or
// without the port. The fake's port stands in for 3876.
func (f *Fake) allowedHost(host string) bool {
	port := f.port
	for _, h := range []string{"127.0.0.1", "localhost"} {
		if host == h || host == fmt.Sprintf("%s:%d", h, port) {
			return true
		}
	}
	return false
}

// authorised mirrors parseBearerToken: a case-insensitive "Bearer" scheme, at
// least one space, then the credential verbatim.
func (f *Fake) authorised(header string) bool {
	const scheme = "bearer"
	if len(header) <= len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return false
	}
	rest := header[len(scheme):]
	credential := strings.TrimLeft(rest, " ")
	if len(credential) == len(rest) || credential == "" {
		return false
	}
	return credential == f.token
}

func (f *Fake) route(w http.ResponseWriter, r *http.Request, raw []byte, body any) {
	segments := strings.FieldsFunc(r.URL.Path, func(c rune) bool { return c == '/' })
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/tasks":
		f.listTasks(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/tasks":
		f.createTask(w, raw, body)
	case len(segments) == 2 && segments[0] == "tasks":
		f.taskRoute(w, r.Method, segments[1], raw, body)
	case r.Method == http.MethodGet && r.URL.Path == "/projects":
		writeData(w, http.StatusOK, f.projects)
	case r.Method == http.MethodGet && r.URL.Path == "/tags":
		writeData(w, http.StatusOK, f.tags)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Route not found")
	}
}

func (f *Fake) listTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	source := q.Get("source")
	if source != "active" && source != "archived" && source != "all" {
		source = "active"
	}
	includeDone := strings.EqualFold(q.Get("includeDone"), "true")
	query := strings.ToLower(q.Get("query"))

	out := []Task{}
	if source == "archived" {
		// The fake keeps no archive.
		writeData(w, http.StatusOK, out)
		return
	}
	for _, task := range f.tasks {
		if query != "" && !strings.Contains(strings.ToLower(task.Title), query) {
			continue
		}
		if !includeDone && task.IsDone {
			continue
		}
		out = append(out, clone(task))
	}
	writeData(w, http.StatusOK, out)
}

var allowedTaskFields = map[string]bool{
	"title": true, "notes": true, "isDone": true, "timeEstimate": true, "timeSpent": true,
	"projectId": true, "tagIds": true, "dueDay": true, "dueWithTime": true, "plannedAt": true,
	"deadlineDay": true, "deadlineWithTime": true, "deadlineRemindAt": true,
}

// writable is the subset of SP's writable fields the fake stores.
type writable struct {
	Title     *string   `json:"title"`
	Notes     *string   `json:"notes"`
	IsDone    *bool     `json:"isDone"`
	ProjectID *string   `json:"projectId"`
	TagIDs    *[]string `json:"tagIds"`
}

// decodeWritable keeps only allowed fields (as pickAllowedFields does) and
// rejects wrong value types (as validateWritableFields does).
func decodeWritable(fields map[string]json.RawMessage) (writable, bool) {
	picked := map[string]json.RawMessage{}
	for k, v := range fields {
		if allowedTaskFields[k] {
			picked[k] = v
		}
	}
	buf, err := json.Marshal(picked)
	if err != nil {
		return writable{}, false
	}
	var out writable
	dec := json.NewDecoder(bytes.NewReader(buf))
	if err := dec.Decode(&out); err != nil {
		return writable{}, false
	}
	if out.TagIDs != nil && *out.TagIDs == nil {
		// "tagIds": null is not a string[].
		return writable{}, false
	}
	return out, true
}

func readIgnoreShortSyntax(fields map[string]json.RawMessage) (bool, bool) {
	raw, ok := fields["isIgnoreShortSyntax"]
	if !ok {
		return false, true
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, false
	}
	return v, true
}

func (f *Fake) createTask(w http.ResponseWriter, raw []byte, body any) {
	fields, isObject := asObject(raw, body)
	var title string
	if isObject {
		_ = json.Unmarshal(fields["title"], &title)
	}
	if !isObject || strings.TrimSpace(title) == "" {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "Task title must be a non-empty string")
		return
	}
	if _, ok := fields["subTaskIds"]; ok {
		writeError(w, http.StatusBadRequest, "UNSUPPORTED_FIELD", "subTaskIds cannot be set on task creation")
		return
	}
	ignoreShortSyntax, ok := readIgnoreShortSyntax(fields)
	if !ok {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "isIgnoreShortSyntax must be a boolean")
		return
	}
	in, ok := decodeWritable(fields)
	if !ok {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "One or more task fields have an invalid type")
		return
	}

	task := f.newTaskWithDefaultsLocked(strings.TrimSpace(title), fields)
	if in.Notes != nil {
		task.Notes = *in.Notes
	}
	if in.IsDone != nil {
		task.IsDone = *in.IsDone
	}
	if in.ProjectID != nil {
		// SP does not check that the project exists on create.
		task.ProjectID = *in.ProjectID
	}
	if in.TagIDs != nil {
		// SP does not check that tag ids exist: unknown ids are stored as sent.
		task.TagIDs = slices.Clone(*in.TagIDs)
	}

	if rawParent, ok := fields["parentId"]; ok {
		var parentID string
		if err := json.Unmarshal(rawParent, &parentID); err != nil || parentID == "" {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "parentId must be a non-empty string")
			return
		}
		for _, inherited := range []string{"projectId", "tagIds"} {
			if _, ok := fields[inherited]; ok {
				writeError(w, http.StatusBadRequest, "UNSUPPORTED_FIELD",
					inherited+" cannot be set when creating a subtask")
				return
			}
		}
		parent := f.findLocked(parentID)
		if parent == nil {
			writeError(w, http.StatusNotFound, "PARENT_NOT_FOUND", "Parent task "+parentID+" not found")
			return
		}
		if parent.ParentID != "" {
			writeError(w, http.StatusBadRequest, "INVALID_PARENT", "Cannot nest subtasks")
			return
		}
		task.ParentID = parentID
		task.ProjectID = parent.ProjectID
		task.TagIDs = []string{}
		parent.SubTaskIDs = append(parent.SubTaskIDs, task.ID)
	}

	if !ignoreShortSyntax {
		f.applyShortSyntaxLocked(&task)
	}
	if rawDueDay, ok := fields["dueDay"]; ok {
		var dueDay *string
		if err := json.Unmarshal(rawDueDay, &dueDay); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "One or more task fields have an invalid type")
			return
		}
		task.DueDay = ""
		if dueDay != nil {
			task.DueDay = *dueDay
		}
	}
	normalise(&task)
	f.tasks = append(f.tasks, task)
	stored := f.findLocked(task.ID)
	if f.onWrite != nil {
		f.onWrite(stored)
	}
	f.writeWriteResult(w, http.StatusCreated, clone(*stored))
}

// newTaskWithDefaultsLocked mirrors createNewTaskWithDefaults in SP's
// task.service.ts: a project view gives its project, otherwise the default
// project setting or the inbox; a tag view other than TODAY gives its tag;
// the TODAY view plans the task for today unless dueDay or dueWithTime is
// sent. Fields in the request then override these defaults.
func (f *Fake) newTaskWithDefaultsLocked(title string, fields map[string]json.RawMessage) Task {
	task := Task{ID: f.newIDLocked(), Title: title, TagIDs: []string{}}
	_, hasParent := fields["parentId"]
	if f.contextKind == ProjectContext {
		task.ProjectID = f.contextID
	} else {
		task.ProjectID = f.defaultProjectID
		if task.ProjectID == "" {
			task.ProjectID = "INBOX_PROJECT"
		}
	}
	if f.contextKind == TagContext && !hasParent && f.contextID != "TODAY" {
		task.TagIDs = []string{f.contextID}
	}
	_, hasDueDay := fields["dueDay"]
	_, hasDueWithTime := fields["dueWithTime"]
	if f.contextID == "TODAY" && !hasParent && !hasDueWithTime && !hasDueDay {
		task.DueDay = time.Now().Format("2006-01-02")
	}
	return task
}

// writeWriteResult answers a POST or PATCH that has been applied, or the
// injected FailWrites error.
func (f *Fake) writeWriteResult(w http.ResponseWriter, status int, task Task) {
	f.wrote = true
	if f.failWrites != nil {
		writeError(w, f.failWrites.status, f.failWrites.code, "injected failure after the write was applied")
		return
	}
	writeData(w, status, task)
}

var shortSyntaxTag = regexp.MustCompile(`(^|\s)#(\w+)`)

// applyShortSyntaxLocked models the one short-syntax rule the tests need: a
// "#word" in the title becomes a tag (created if missing) and leaves the title.
func (f *Fake) applyShortSyntaxLocked(task *Task) {
	for _, m := range shortSyntaxTag.FindAllStringSubmatch(task.Title, -1) {
		word := m[2]
		id := ""
		for _, t := range f.tags {
			if strings.EqualFold(t.Title, word) {
				id = t.ID
			}
		}
		if id == "" {
			id = "tag-" + word
			f.tags = append(f.tags, tag{ID: id, Title: word, TaskIDs: []string{}})
		}
		task.TagIDs = append(task.TagIDs, id)
	}
	task.Title = strings.Join(strings.Fields(shortSyntaxTag.ReplaceAllString(task.Title, " ")), " ")
}

func (f *Fake) taskRoute(w http.ResponseWriter, method, id string, raw []byte, body any) {
	switch method {
	case http.MethodGet:
		task := f.findLocked(id)
		if task == nil {
			writeError(w, http.StatusNotFound, "TASK_NOT_FOUND", "Task not found")
			return
		}
		writeData(w, http.StatusOK, clone(*task))
	case http.MethodPatch:
		f.patchTask(w, id, raw, body)
	case http.MethodDelete:
		task := f.findLocked(id)
		if task == nil {
			writeError(w, http.StatusNotFound, "TASK_NOT_FOUND", "Task not found")
			return
		}
		remove := append([]string{id}, task.SubTaskIDs...)
		f.tasks = slices.DeleteFunc(f.tasks, func(t Task) bool { return slices.Contains(remove, t.ID) })
		writeData(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Route not found")
	}
}

func (f *Fake) patchTask(w http.ResponseWriter, id string, raw []byte, body any) {
	fields, isObject := asObject(raw, body)
	if !isObject {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "PATCH body must be a JSON object")
		return
	}
	if _, ok := readIgnoreShortSyntax(fields); !ok {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "isIgnoreShortSyntax must be a boolean")
		return
	}
	for _, rejected := range []string{"parentId", "subTaskIds"} {
		if _, ok := fields[rejected]; ok {
			writeError(w, http.StatusBadRequest, "UNSUPPORTED_FIELD",
				rejected+" cannot be set via PATCH - re-parenting is not supported by this API")
			return
		}
	}
	in, ok := decodeWritable(fields)
	if !ok {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "One or more task fields have an invalid type")
		return
	}
	task := f.findLocked(id)
	if task == nil {
		writeError(w, http.StatusNotFound, "TASK_NOT_FOUND", "Task not found")
		return
	}
	if in.Title != nil {
		task.Title = *in.Title
	}
	if in.Notes != nil {
		task.Notes = *in.Notes
	}
	if in.IsDone != nil {
		task.IsDone = *in.IsDone
		if task.IsDone {
			doneOn := int64(1)
			task.DoneOn = &doneOn
		} else {
			task.DoneOn = nil
		}
	}
	if in.TagIDs != nil {
		// tagIds is a full replacement.
		task.TagIDs = slices.Clone(*in.TagIDs)
	}
	normalise(task)
	if f.onWrite != nil {
		f.onWrite(task)
	}
	f.writeWriteResult(w, http.StatusOK, clone(*task))
}

func asObject(raw []byte, body any) (map[string]json.RawMessage, bool) {
	if _, ok := body.(map[string]any); !ok {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, false
	}
	return fields, true
}

func (f *Fake) newIDLocked() string {
	f.nextID++
	return fmt.Sprintf("task-%d", f.nextID)
}

func (f *Fake) findLocked(id string) *Task {
	for i := range f.tasks {
		if f.tasks[i].ID == id {
			return &f.tasks[i]
		}
	}
	return nil
}

func normalise(task *Task) {
	if task.TagIDs == nil {
		task.TagIDs = []string{}
	}
	if task.SubTaskIDs == nil {
		task.SubTaskIDs = []string{}
	}
	if task.TimeSpentOnDay == nil {
		task.TimeSpentOnDay = map[string]int64{}
	}
	if task.Attachments == nil {
		task.Attachments = []any{}
	}
}

func clone(task Task) Task {
	task.TagIDs = slices.Clone(task.TagIDs)
	task.SubTaskIDs = slices.Clone(task.SubTaskIDs)
	normalise(&task)
	return task
}

func writeData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, map[string]any{"ok": true, "data": data})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": map[string]any{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	buf, err := json.Marshal(body)
	if err != nil {
		status = http.StatusInternalServerError
		buf = []byte(`{"ok":false,"error":{"code":"INTERNAL_ERROR","message":"fake could not encode its response"}}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf)
}
