package feishu

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/smallnest/pigo/internal/cli/config"
	"github.com/smallnest/pigo/internal/provider"
)

// credentialName is the entry Feishu-set API keys use inside
// .credentials.yaml. config.toml stores only this name.
const credentialName = "feishu"

// Binding is the saved Feishu app created by the QR device flow, plus the
// pigo session that continues the bound user's chat across restarts.
// AppSecret is a credential: the file is mode 0600.
type Binding struct {
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
	OpenID    string `json:"open_id"`
	SessionID string `json:"session_id"`
}

func (b Binding) usable() bool {
	return strings.TrimSpace(b.AppID) != "" && strings.TrimSpace(b.AppSecret) != ""
}

// LoadBinding reads the binding file. A missing file is an empty binding and
// a nil error.
func LoadBinding(path string) (Binding, error) {
	if path == "" {
		return Binding{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Binding{}, nil
		}
		return Binding{}, err
	}
	var b Binding
	if err := json.Unmarshal(data, &b); err != nil {
		return Binding{}, fmt.Errorf("parse feishu binding %s: %w", path, err)
	}
	return b, nil
}

// SaveBinding writes the binding mode 0600 so the app secret is not
// group-readable.
func SaveBinding(path string, b Binding) error {
	if path == "" {
		return errors.New("feishu binding path is empty")
	}
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// defaultBindPath is $PIGO_HOME/feishu.json, or ~/.pigo/feishu.json.
func defaultBindPath() string {
	dir := os.Getenv("PIGO_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".pigo")
	}
	return filepath.Join(dir, "feishu.json")
}

// fileStore persists the model API base URL and API key the way the rest of
// pigo reads them: base_url (and protocol/model) in config.toml, the secret
// only in the 0600 credential file under the name "feishu".
type fileStore struct {
	configPath string
	credPath   string
}

func (f fileStore) saveDoc(update func(map[string]any)) error {
	return config.UpdateFileConfig(f.configPath, update)
}

func (f fileStore) SaveBase(baseURL, protocol string) error {
	return f.saveDoc(func(doc map[string]any) {
		doc["base_url"] = baseURL
		if protocol != "" {
			doc["protocol"] = protocol
		}
	})
}

func (f fileStore) SaveModel(model string) error {
	return f.saveDoc(func(doc map[string]any) {
		doc["model"] = model
	})
}

// SaveKey stores the secret in the credential file and points config.toml at
// it. A previous plaintext api_key in the toml is removed so the file no
// longer holds the secret (the credential reference wins only when api_key
// is empty).
func (f fileStore) SaveKey(secret string) error {
	if err := provider.SaveCredential(f.credPath, credentialName, secret); err != nil {
		return err
	}
	return f.saveDoc(func(doc map[string]any) {
		doc["credential"] = credentialName
		delete(doc, "api_key")
	})
}
