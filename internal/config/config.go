// Package config owns the ~/.rchrome directory: keys, the saved connection and logs.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// DirName is the per-user state directory under $HOME.
const DirName = ".rchrome"

// ErrNoConnect is returned when connect.json does not exist yet.
var ErrNoConnect = errors.New("还没有执行过 rchrome connect / no saved connection; run `rchrome connect <addr>` first")

// Dir returns ~/.rchrome, creating it with 0700 if needed.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(home, DirName)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}

func sub(name string) (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name), nil
}

func subDir(name string) (string, error) {
	p, err := sub(name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(p, 0o700); err != nil {
		return "", err
	}
	return p, nil
}

// ServerKeyPath is the persistent server identity (keeps the tc… address stable).
func ServerKeyPath() (string, error) { return sub("server.key.json") }

// ClientKeyPath is the persistent client identity (for `serve --allow`).
func ClientKeyPath() (string, error) { return sub("client.key.json") }

// ConnectPath stores the last successful `rchrome connect`.
func ConnectPath() (string, error) { return sub("connect.json") }

// ProfileDir is the Chrome user-data-dir used by `rchrome serve`.
func ProfileDir() (string, error) { return subDir("profile") }

// LogsDir holds serve-*.log and mcp-*.log.
func LogsDir() (string, error) { return subDir("logs") }

// Connect is what `rchrome connect` saves and `rchrome mcp` reads back.
type Connect struct {
	Name       string `json:"name,omitempty"`
	Label      string `json:"label,omitempty"`
	Addr       string `json:"addr"`
	RemotePort int    `json:"remote_port"`
	LocalPort  int    `json:"local_port"`
	Backend    string `json:"backend"`
	OS         string `json:"os,omitempty"`
	Browser    string `json:"browser,omitempty"`
}

// LoadConnect reads connect.json.
func LoadConnect() (*Connect, error) {
	p, err := ConnectPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoConnect
	}
	if err != nil {
		return nil, err
	}
	var c Connect
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// SaveConnect writes connect.json with 0600.
func SaveConnect(c *Connect) error {
	p, err := ConnectPath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}
