// Package sp is the adapter to SP desktop's Local REST API (Super Productivity
// v19.2.0). It implements tools.Desktop.
package sp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/grez-lucas/sp-quadrant-mcp/internal/tools"
)

// DefaultBaseURL is where SP desktop serves the Local REST API. The port is
// fixed and the host is one SP accepts in its Host header check.
const DefaultBaseURL = "http://127.0.0.1:3876"

const (
	requestTimeout = 20 * time.Second
	// SP caps request bodies at 1 MiB; a task list can be larger, so the
	// response cap is generous but bounded.
	maxResponseBytes = 64 << 20
)

// Client talks to SP desktop. It never reads the token itself: the caller
// passes it in, and it is sent only in the Authorization header.
type Client struct {
	baseURL *url.URL
	token   string
	http    *http.Client
}

var _ tools.Desktop = (*Client)(nil)

// New returns a Client for the Local REST API at baseURL, authenticating with
// token. With an empty token every call fails with tools.ErrNoToken before
// touching the network.
func New(baseURL, token string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse SP base URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("SP base URL %q is not an http(s) URL with a host", baseURL)
	}
	transport := &http.Transport{
		// Never send the token through a proxy, whatever the environment says.
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: requestTimeout,
	}
	return &Client{
		baseURL: u,
		token:   token,
		http:    &http.Client{Transport: transport, Timeout: requestTimeout},
	}, nil
}

// Health calls GET /health, which SP serves without authentication.
func (c *Client) Health(ctx context.Context) (tools.Health, error) {
	var out struct {
		RendererReady bool `json:"rendererReady"`
	}
	if err := c.do(ctx, http.MethodGet, "/health", nil, nil, false, &out); err != nil {
		return tools.Health{}, err
	}
	return tools.Health{RendererReady: out.RendererReady}, nil
}

type idTitle struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Tags calls GET /tags.
func (c *Client) Tags(ctx context.Context) ([]tools.Tag, error) {
	var out []idTitle
	if err := c.do(ctx, http.MethodGet, "/tags", nil, nil, true, &out); err != nil {
		return nil, err
	}
	tags := make([]tools.Tag, 0, len(out))
	for _, t := range out {
		tags = append(tags, tools.Tag(t))
	}
	return tags, nil
}

// Projects calls GET /projects.
func (c *Client) Projects(ctx context.Context) ([]tools.Project, error) {
	var out []idTitle
	if err := c.do(ctx, http.MethodGet, "/projects", nil, nil, true, &out); err != nil {
		return nil, err
	}
	projects := make([]tools.Project, 0, len(out))
	for _, p := range out {
		projects = append(projects, tools.Project(p))
	}
	return projects, nil
}

// task is the part of SP's task JSON the tools use.
type task struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	IsDone    bool     `json:"isDone"`
	TagIDs    []string `json:"tagIds"`
	ProjectID string   `json:"projectId"`
	// Absent for a top-level task; null in some older data.
	ParentID *string `json:"parentId"`
}

func (t task) toTools() tools.Task {
	out := tools.Task{
		ID:        t.ID,
		Title:     t.Title,
		Done:      t.IsDone,
		TagIDs:    t.TagIDs,
		ProjectID: t.ProjectID,
	}
	if out.TagIDs == nil {
		out.TagIDs = []string{}
	}
	if t.ParentID != nil {
		out.ParentID = *t.ParentID
	}
	return out
}

// ListTasks calls GET /tasks?source=active, so archived tasks never appear.
// SP returns subtasks too.
func (c *Client) ListTasks(ctx context.Context, filter tools.ListFilter) ([]tools.Task, error) {
	q := url.Values{"source": {"active"}}
	if filter.Query != "" {
		q.Set("query", filter.Query)
	}
	if filter.IncludeDone {
		q.Set("includeDone", "true")
	}
	var out []task
	if err := c.do(ctx, http.MethodGet, "/tasks", q, nil, true, &out); err != nil {
		return nil, err
	}
	tasks := make([]tools.Task, 0, len(out))
	for _, t := range out {
		tasks = append(tasks, t.toTools())
	}
	return tasks, nil
}

// GetTask calls GET /tasks/:id.
func (c *Client) GetTask(ctx context.Context, id string) (tools.Task, error) {
	var out task
	if err := c.do(ctx, http.MethodGet, "/tasks/"+url.PathEscape(id), nil, nil, true, &out); err != nil {
		return tools.Task{}, err
	}
	return out.toTools(), nil
}

// CreateTask calls POST /tasks. It always sends isIgnoreShortSyntax, so SP
// stores the title literally instead of parsing "#tag", "+project" or "30m"
// out of it.
func (c *Client) CreateTask(ctx context.Context, nt tools.NewTask) (tools.Task, error) {
	tagIDs := nt.TagIDs
	if tagIDs == nil {
		tagIDs = []string{}
	}
	body := map[string]any{
		"title":               nt.Title,
		"projectId":           nt.ProjectID,
		"tagIds":              tagIDs,
		"isIgnoreShortSyntax": true,
	}
	var out task
	if err := c.do(ctx, http.MethodPost, "/tasks", nil, body, true, &out); err != nil {
		return tools.Task{}, err
	}
	return out.toTools(), nil
}

// UpdateTask calls PATCH /tasks/:id. A non-nil TagIDs replaces the whole tag
// list, which is what SP does with tagIds on every write.
func (c *Client) UpdateTask(ctx context.Context, id string, ch tools.TaskChanges) (tools.Task, error) {
	body := map[string]any{}
	if ch.TagIDs != nil {
		body["tagIds"] = ch.TagIDs
	}
	if ch.Done != nil {
		body["isDone"] = *ch.Done
	}
	var out task
	if err := c.do(ctx, http.MethodPatch, "/tasks/"+url.PathEscape(id), nil, body, true, &out); err != nil {
		return tools.Task{}, err
	}
	return out.toTools(), nil
}

// DeleteTask calls DELETE /tasks/:id. It is not part of tools.Desktop: no tool
// deletes, and only the live contract suite uses it to clean up after itself.
func (c *Client) DeleteTask(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/tasks/"+url.PathEscape(id), nil, nil, true, nil)
}

// TaskFields calls GET /tasks/:id and returns every field SP sent, undecoded.
// It is not part of tools.Desktop: only the live contract suite uses it, to
// check fields the tools never read (such as timeEstimate).
func (c *Client) TaskFields(ctx context.Context, id string) (map[string]json.RawMessage, error) {
	var out map[string]json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/tasks/"+url.PathEscape(id), nil, nil, true, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, authenticated bool, out any) error {
	// Refused even for GET /health, which SP serves without a token: without
	// one no tool call can succeed until Claude Code restarts the server, so
	// the missing token is the one answer worth giving, whether or not SP is
	// running. Probing first would only trade it for "not running" or
	// "starting" messages whose fixes do not help.
	if c.token == "" {
		return tools.ErrNoToken
	}
	u := c.baseURL.JoinPath(path)
	if query != nil {
		u.RawQuery = query.Encode()
	}

	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s %s body: %w", method, path, err)
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reqBody)
	if err != nil {
		return fmt.Errorf("build %s %s: %w", method, path, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%s %s: %w", method, path, ctxErr)
		}
		return fmt.Errorf("%w: %s %s: %w", tools.ErrUnreachable, method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%w: reading %s %s response: %w", tools.ErrUnreachable, method, path, err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%s %s: SP answered HTTP %d with a body that is not its JSON envelope", method, path, resp.StatusCode)
	}
	if !env.OK {
		if env.Error == nil {
			return fmt.Errorf("%s %s: SP answered HTTP %d with ok=false and no error", method, path, resp.StatusCode)
		}
		return &tools.SPError{Status: resp.StatusCode, Code: env.Error.Code, Message: env.Error.Message}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s %s: SP answered HTTP %d with ok=true", method, path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("decode %s %s data: %w", method, path, err)
	}
	return nil
}
