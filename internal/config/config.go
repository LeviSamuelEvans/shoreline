package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const appName = "shoreline"

type Config struct {
	Dir        string
	ConfigPath string
	StatePath  string
	DBPath     string
}

// UserConfig is non-secret, user-editable configuration.
// Secrets (client secrets, tokens) must NOT be stored here.
type UserConfig struct {
	SoundCloud UserSoundCloudConfig `json:"soundcloud"`
}

type UserSoundCloudConfig struct {
	RedirectURL string `json:"redirect_url"`
}

type State struct {
	SoundCloud SoundCloudState `json:"soundcloud"`
}

type SoundCloudState struct {
	RedirectURL string     `json:"redirect_url"`
	Account     *Account   `json:"account,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
}

type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Scope        string    `json:"scope"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type Account struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Permalink string `json:"permalink"`
}

type StateStore struct {
	path string
}

func NewStateStore(path string) *StateStore {
	return &StateStore{path: path}
}

func Load() (Config, error) {
	if override := os.Getenv("SHORELINE_CONFIG_DIR"); override != "" {
		dir := filepath.Clean(override)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Config{}, fmt.Errorf("create config dir: %w", err)
		}

		return Config{
			Dir:        dir,
			ConfigPath: filepath.Join(dir, "config.json"),
			StatePath:  filepath.Join(dir, "state.json"),
			DBPath:     filepath.Join(dir, "shoreline.db"),
		}, nil
	}

	base, err := os.UserConfigDir()
	if err != nil {
		return Config{}, fmt.Errorf("resolve config dir: %w", err)
	}

	dir := filepath.Join(base, appName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Config{}, fmt.Errorf("create config dir: %w", err)
	}

	return Config{
		Dir:        dir,
		ConfigPath: filepath.Join(dir, "config.json"),
		StatePath:  filepath.Join(dir, "state.json"),
		DBPath:     filepath.Join(dir, "shoreline.db"),
	}, nil
}

func (s *StateStore) Load() (State, error) {
	var state State

	// Pull non-secret defaults from user config first.
	// Precedence: env > config.json > state.json (legacy) > default.
	cfgDir := filepath.Dir(s.path)
	userCfg, _ := LoadUserConfig(filepath.Join(cfgDir, "config.json"))
	if userCfg.SoundCloud.RedirectURL != "" {
		state.SoundCloud.RedirectURL = userCfg.SoundCloud.RedirectURL
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			state.SoundCloud.RedirectURL = redirectURLFromEnv(state.SoundCloud.RedirectURL)
			return state, nil
		}
		return State{}, fmt.Errorf("read state file: %w", err)
	}

	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("decode state file: %w", err)
	}

	// Prefer config.json redirect_url over legacy state.json value.
	if userCfg.SoundCloud.RedirectURL != "" {
		state.SoundCloud.RedirectURL = userCfg.SoundCloud.RedirectURL
	}
	state.SoundCloud.RedirectURL = redirectURLFromEnv(state.SoundCloud.RedirectURL)

	return state, nil
}

func (s *StateStore) Save(state State) error {
	now := time.Now().UTC()
	state.SoundCloud.UpdatedAt = &now

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state file: %w", err)
	}

	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}

	return nil
}

const defaultRedirectURL = "http://127.0.0.1:9876/callback"

func redirectURLFromEnv(fallback string) string {
	if v := os.Getenv("SHORELINE_SOUNDCLOUD_REDIRECT_URL"); v != "" {
		return v
	}
	if fallback != "" {
		return fallback
	}
	return defaultRedirectURL
}

func LoadUserConfig(path string) (UserConfig, error) {
	var cfg UserConfig

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return UserConfig{}, fmt.Errorf("read user config: %w", err)
	}
	if len(data) == 0 {
		return cfg, nil
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return UserConfig{}, fmt.Errorf("decode user config: %w", err)
	}

	return cfg, nil
}
