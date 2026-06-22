package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shandley/trellis/internal/api"
)

// Config is the on-disk client configuration, shared in format with the mcp
// package. It is stored as JSON at $XDG_CONFIG_HOME/trellis/config.json
// (falling back to ~/.config/trellis/config.json).
type Config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

// configPath returns the absolute path to the config file, honoring
// XDG_CONFIG_HOME and falling back to ~/.config.
func configPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "trellis", "config.json"), nil
}

// readConfigFile loads the config file. A missing file is not an error: it
// returns a zero Config and ok=false.
func readConfigFile() (Config, bool) {
	path, err := configPath()
	if err != nil {
		return Config{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, false
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, false
	}
	return c, true
}

// writeConfigFile persists the config, creating the parent directory.
func writeConfigFile(c Config) (string, error) {
	path, err := configPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write config: %w", err)
	}
	return path, nil
}

// loadConfig resolves the (server, token) pair from, in order of precedence:
// environment variables (TRELLIS_SERVER / TRELLIS_TOKEN), the config file, then
// the built-in default for the server. The token has no default.
func loadConfig() (server, token string) {
	file, _ := readConfigFile()

	server = file.Server
	token = file.Token

	if v := os.Getenv("TRELLIS_SERVER"); v != "" {
		server = v
	}
	if v := os.Getenv("TRELLIS_TOKEN"); v != "" {
		token = v
	}

	if server == "" {
		server = api.DefaultBaseURL
	}
	return server, token
}
