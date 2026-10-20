package tools

import (
	"context"
	"errors"
	"fmt"
)

// Desktop is SP desktop, as far as the tools need it. The sp package
// implements it over SP desktop's Local REST API.
type Desktop interface {
	// Health probes SP without credentials.
	Health(ctx context.Context) (Health, error)
	// Tags lists the tag entities of the sync account.
	Tags(ctx context.Context) ([]Tag, error)
	// Projects lists the projects of the sync account.
	Projects(ctx context.Context) ([]Project, error)
	// ListTasks lists active (never archived) tasks, subtasks included.
	ListTasks(ctx context.Context, filter ListFilter) ([]Task, error)
	// GetTask reads one task by id.
	GetTask(ctx context.Context, id string) (Task, error)
	// CreateTask creates a task, storing its title literally.
	CreateTask(ctx context.Context, task NewTask) (Task, error)
	// UpdateTask applies changes to a task and returns it as SP stored it.
	UpdateTask(ctx context.Context, id string, changes TaskChanges) (Task, error)
}

// Health is SP's answer to GET /health.
type Health struct {
	RendererReady bool
}

// Tag is a tag entity in the sync account.
type Tag struct {
	ID    string
	Title string
}

// Project is a project in the sync account.
type Project struct {
	ID    string
	Title string
}

// Task is an SP task as the REST API returns it: a Task in the domain sense
// when ParentID is empty, a subtask otherwise.
type Task struct {
	ID        string
	Title     string
	Done      bool
	TagIDs    []string
	ProjectID string
	ParentID  string
}

// IsSubtask reports whether t is a subtask, which never appears on the board.
func (t Task) IsSubtask() bool { return t.ParentID != "" }

// ListFilter narrows ListTasks.
type ListFilter struct {
	// Query keeps tasks whose title contains it, case-insensitively.
	Query string
	// IncludeDone keeps completed tasks too.
	IncludeDone bool
}

// NewTask is a task to create.
type NewTask struct {
	Title     string
	ProjectID string
	TagIDs    []string
}

// TaskChanges is a partial update to a task.
type TaskChanges struct {
	// TagIDs replaces the task's whole tag list when non-nil, even when empty.
	// Nil leaves the tags alone.
	TagIDs []string
	// Done sets the task's done flag when non-nil.
	Done *bool
}

// Errors a Desktop reports, which the tools turn into messages for the user.
var (
	// ErrUnreachable is a transport failure: nothing answered, or the
	// connection broke before an answer arrived.
	ErrUnreachable = errors.New("SP desktop did not answer")
	// ErrNoToken means no Local REST API token was available at startup.
	ErrNoToken = errors.New("no Local REST API token")
)

// SPError is an error envelope SP returned.
type SPError struct {
	Status  int
	Code    string
	Message string
}

func (e *SPError) Error() string {
	return fmt.Sprintf("SP error %s (HTTP %d): %s", e.Code, e.Status, e.Message)
}
