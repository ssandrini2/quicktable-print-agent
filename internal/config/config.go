// Package config is what the agent keeps between runs: where the API is and
// its pairing token.
package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Config is the agent's state file.
type Config struct {
	// APIURL overrides the URL the agent was built with (support use).
	APIURL string `json:"apiUrl,omitempty"`
	// Token is the pairing token, encrypted for this Windows user (see protect).
	Token string `json:"token,omitempty"`
	// Lang is the language chosen at install ("es" or "en").
	Lang string `json:"lang,omitempty"`
	// FailedUpdate is an update that was tried and undone: it isn't tried
	// again, and the API is told.
	FailedUpdate *FailedUpdate `json:"failedUpdate,omitempty"`
}

// FailedUpdate records an update the agent went back from.
type FailedUpdate struct {
	// Version is the one that failed.
	Version string `json:"version"`
	Error   string `json:"error"`
	// From is the version that was (and stayed) running. Once the agent runs
	// another one, the record is stale.
	From string `json:"from"`
}

// Store reads and writes the state file in the agent's data directory.
type Store struct {
	Dir string
}

// DefaultDir is the agent's per-user data directory:
// %LOCALAPPDATA%\QuickTable\PrintAgent on Windows.
func DefaultDir() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		var err error
		if base, err = os.UserConfigDir(); err != nil {
			return "", err
		}
	}
	return filepath.Join(base, "QuickTable", "PrintAgent"), nil
}

func (s Store) path() string { return filepath.Join(s.Dir, "config.json") }

// Load returns the saved config, or an empty one if there is none yet.
func (s Store) Load() (Config, error) {
	var config Config
	raw, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	err = json.Unmarshal(raw, &config)
	return config, err
}

// Save writes the config, replacing the file in one step.
func (s Store) Save(config Config) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	temp := s.path() + ".tmp"
	if err := os.WriteFile(temp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, s.path())
}

// PairingToken returns the pairing token, or "" when unpaired — also when the saved
// one can't be read back (another Windows user's file): the agent pairs again.
func (c Config) PairingToken() string {
	if c.Token == "" {
		return ""
	}
	sealed, err := base64.StdEncoding.DecodeString(c.Token)
	if err != nil {
		return ""
	}
	token, err := unprotect(sealed)
	if err != nil {
		return ""
	}
	return string(token)
}

// SetPairingToken stores the token encrypted; "" clears it.
func (c *Config) SetPairingToken(token string) error {
	if token == "" {
		c.Token = ""
		return nil
	}
	sealed, err := protect([]byte(token))
	if err != nil {
		return err
	}
	c.Token = base64.StdEncoding.EncodeToString(sealed)
	return nil
}
