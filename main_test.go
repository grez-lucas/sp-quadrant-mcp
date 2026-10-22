package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/grez-lucas/sp-quadrant-mcp/internal/sptest"
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// start runs the server the way main does, but over an in-memory transport,
// with the token read from tokenPath and SP at baseURL. It returns a connected
// client session and the server's log output.
func start(t *testing.T, tokenPath, baseURL string) (*mcp.ClientSession, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, config{
			tokenPath: func() (string, error) { return tokenPath, nil },
			baseURL:   baseURL,
			transport: serverTransport,
			logger:    logger,
		})
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("run returned %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("run did not return after the client closed")
		}
	})
	return session, logs
}

func callText(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n"), res.IsError
}

func TestWithoutATokenFileTheServerStartsAndEveryToolSaysToEnableTheAPI(t *testing.T) {
	fake := sptest.New(t, "irrelevantFFFFFFFFFFFFFFFFFFFFFF")
	fake.AddQuadrantTags()
	missing := filepath.Join(t.TempDir(), "superProductivity", "local-rest-api-token")

	session, logs := start(t, missing, fake.URL())

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(listed.Tools) != 5 {
		t.Fatalf("server lists %d tools, want 5", len(listed.Tools))
	}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"list_tasks", nil},
		{"create_task", map[string]any{"title": "x", "urgent": true, "important": true}},
		{"move_task", map[string]any{"task_id": "x", "urgent": true, "important": true}},
		{"complete_task", map[string]any{"task_id": "x"}},
		{"add_notes", map[string]any{"task_id": "x", "text": "- [ ] y"}},
	} {
		msg, isError := callText(t, session, c.tool, c.args)
		if !isError {
			t.Fatalf("%s succeeded without a token", c.tool)
		}
		for _, want := range []string{"Enable local REST API", "restart Claude Code"} {
			if !strings.Contains(msg, want) {
				t.Fatalf("%s error %q does not mention %q", c.tool, msg, want)
			}
		}
	}
	if n := len(fake.Requests()); n != 0 {
		t.Fatalf("%d requests reached SP without a token", n)
	}
	if !strings.Contains(logs.String(), missing) {
		t.Fatalf("the log does not say which token file was missing:\n%s", logs.String())
	}
}

func TestTheTokenNeverAppearsInLogsOrToolErrors(t *testing.T) {
	const secret = "s3cretTokenGGGGGGGGGGGGGGGGGGGGG"
	dir := t.TempDir()
	path := filepath.Join(dir, "local-rest-api-token")
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// SP accepts the token: the call succeeds, which proves the token was
	// loaded and sent.
	accepting := sptest.New(t, secret)
	accepting.AddQuadrantTags()
	session, logs := start(t, path, accepting.URL())
	if msg, isError := callText(t, session, "create_task", map[string]any{"title": "Pay rent", "urgent": true, "important": true}); isError {
		t.Fatalf("create_task with the right token failed: %s", msg)
	}
	sent := accepting.Requests()[len(accepting.Requests())-1].Header.Get("Authorization")
	if sent != "Bearer "+secret {
		t.Fatalf("the loaded token was not the one sent to SP")
	}

	// SP rejects the token, then goes away.
	rejecting := sptest.New(t, "anotherTokenHHHHHHHHHHHHHHHHHHHH")
	rejecting.AddQuadrantTags()
	rejected, rejectedLogs := start(t, path, rejecting.URL())
	unauthorized, _ := callText(t, rejected, "list_tasks", nil)
	rejecting.Close()
	unreachable, _ := callText(t, rejected, "list_tasks", nil)

	for name, s := range map[string]string{
		"accepted-run log": logs.String(),
		"rejected-run log": rejectedLogs.String(),
		"401 tool error":   unauthorized,
		"down tool error":  unreachable,
	} {
		if s == "" && strings.Contains(name, "log") {
			t.Fatalf("%s is empty; the test would prove nothing", name)
		}
		if strings.Contains(s, secret) {
			t.Fatalf("%s contains the token", name)
		}
	}
	if !strings.Contains(unauthorized, "SP rejected the token") {
		t.Fatalf("401 error = %q", unauthorized)
	}
}

func TestVersionFallsBackToDevel(t *testing.T) {
	// Under `go test` the main module has no version stamped.
	if got := version(); got != "(devel)" {
		t.Fatalf("version() = %q, want (devel)", got)
	}
}
