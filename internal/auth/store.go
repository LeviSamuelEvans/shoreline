package auth

import (
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/levievans/shoreline/internal/config"
)

// NewDefaultSecretStore returns the best-available secret store for this
// platform, preferring macOS Keychain when available and falling back to a
// 0600-permissions file under the config directory otherwise.
func NewDefaultSecretStore(cfg config.Config) SecretStore {
	if runtime.GOOS == "darwin" {
		if _, err := exec.LookPath("security"); err == nil {
			return NewKeychainStore()
		}
	}

	return NewFileStore(filepath.Join(cfg.Dir, "secrets.json"))
}
