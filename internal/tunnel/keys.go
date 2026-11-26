package tunnel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
)

// LoadOrCreateServerKey loads the persistent server identity, generating one on
// first use. A fresh key has RegionID -1 so the first Start probes the nearest
// relay; StartServer then pins that region back into the file so the tc…
// address never changes afterwards.
func LoadOrCreateServerKey(path string) (k *tailcat.PrivateKey, created bool, err error) {
	b, err := os.ReadFile(path)
	if err == nil {
		var pk tailcat.PrivateKey
		if err := json.Unmarshal(b, &pk); err != nil {
			return nil, false, fmt.Errorf("解析 %s 失败 / parse failed: %w", path, err)
		}
		return &pk, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	pk := tailcat.NewPrivateKey()
	pk.Public.RegionID = -1
	if err := SaveServerKey(path, pk); err != nil {
		return nil, false, err
	}
	return pk, true, nil
}

// SaveServerKey writes the identity with 0600.
func SaveServerKey(path string, k *tailcat.PrivateKey) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(k, "", "\t")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

type clientKeyFile struct {
	Private key.NodePrivate `json:"private"`
}

// LoadOrCreateClientKey returns a persistent client identity so a server can
// pin this machine with `serve --allow nodekey:…`.
func LoadOrCreateClientKey(path string) (key.NodePrivate, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		var f clientKeyFile
		if err := json.Unmarshal(b, &f); err != nil {
			return key.NodePrivate{}, fmt.Errorf("解析 %s 失败 / parse failed: %w", path, err)
		}
		if f.Private.IsZero() {
			return key.NodePrivate{}, fmt.Errorf("%s: empty key", path)
		}
		return f.Private, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return key.NodePrivate{}, err
	}
	f := clientKeyFile{Private: key.NewNode()}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return key.NodePrivate{}, err
	}
	out, err := json.MarshalIndent(f, "", "\t")
	if err != nil {
		return key.NodePrivate{}, err
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return key.NodePrivate{}, err
	}
	return f.Private, nil
}
