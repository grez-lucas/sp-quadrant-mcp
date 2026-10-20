package token_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/grez-lucas/sp-quadrant-mcp/internal/token"
)

func TestDefaultPathIsSPsTokenFileUnderTheUserConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	got, err := token.DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if want := filepath.Join(dir, "superProductivity", "local-rest-api-token"); got != want {
		t.Fatalf("DefaultPath = %q, want %q", got, want)
	}
}

func TestLoadTrimsTheFileContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local-rest-api-token")
	if err := os.WriteFile(path, []byte("  abcDEF0123456789abcDEF0123456789\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := token.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != "abcDEF0123456789abcDEF0123456789" {
		t.Fatalf("Load returned the wrong token")
	}
}

func TestLoadOfAMissingFileIsNotExist(t *testing.T) {
	_, err := token.Load(filepath.Join(t.TempDir(), "absent"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestLoadOfAnEmptyFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local-rest-api-token")
	if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := token.Load(path); err == nil {
		t.Fatalf("Load of a blank file returned no error")
	}
}
