// Package token reads the Local REST API token SP desktop writes into its own
// userData directory. The token is a credential: nothing in this package puts
// it in an error or a log line.
package token

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultPath is SP desktop's token file: local-rest-api-token in SP's
// Electron userData directory, which is "superProductivity" under the user
// config directory (~/.config on Linux).
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate the user config directory: %w", err)
	}
	return filepath.Join(dir, "superProductivity", "local-rest-api-token"), nil
}

// ErrEmpty means the token file holds nothing but whitespace.
var ErrEmpty = errors.New("token file is empty")

// Load reads the token file at path and returns its trimmed content.
func Load(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return "", fmt.Errorf("%s: %w", path, ErrEmpty)
	}
	return tok, nil
}
