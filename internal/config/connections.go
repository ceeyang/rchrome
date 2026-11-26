package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidName reports whether name is safe as an MCP server name and a file name.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// ConnectionsDir holds one JSON file per named remote.
func ConnectionsDir() (string, error) { return subDir("connections") }

// SaveConnection stores a named remote (connections/<name>.json).
func SaveConnection(c *Connect) error {
	if !ValidName(c.Name) {
		return errors.New("名称只能用字母、数字、. _ -，且以字母或数字开头 / invalid name")
	}
	d, err := ConnectionsDir()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d, c.Name+".json"), b, 0o600)
}

// LoadConnection reads connections/<name>.json.
func LoadConnection(name string) (*Connect, error) {
	if !ValidName(name) {
		return nil, errors.New("invalid name")
	}
	d, err := ConnectionsDir()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(d, name+".json"))
	if err != nil {
		return nil, err
	}
	var c Connect
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// ListConnections returns every saved remote, sorted by name.
func ListConnections() ([]*Connect, error) {
	d, err := ConnectionsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(d)
	if err != nil {
		return nil, err
	}
	var out []*Connect
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		c, err := LoadConnection(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
