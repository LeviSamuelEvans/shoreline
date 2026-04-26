package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// FileStore is a simple secrets store intended as a fallback when platform
// keychains are unavailable. It stores secrets in a JSON file with 0600 perms.

type FileStore struct {
	path string
}

func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

func (s *FileStore) Get(name string) (string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrSecretNotFound
		}
		return "", fmt.Errorf("read secrets file: %w", err)
	}

	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return "", fmt.Errorf("decode secrets file: %w", err)
	}

	v, ok := m[name]
	if !ok || v == "" {
		return "", ErrSecretNotFound
	}
	return v, nil
}

func (s *FileStore) Set(name, value string) error {
	m, err := s.loadAll()
	if err != nil {
		return err
	}
	if m == nil {
		m = map[string]string{}
	}
	m[name] = value
	return s.saveAll(m)
}

func (s *FileStore) Delete(name string) error {
	m, err := s.loadAll()
	if err != nil {
		return err
	}
	if m == nil {
		return ErrSecretNotFound
	}
	if _, ok := m[name]; !ok {
		return ErrSecretNotFound
	}
	delete(m, name)
	return s.saveAll(m)
}

func (s *FileStore) loadAll() (map[string]string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("read secrets file: %w", err)
	}

	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode secrets file: %w", err)
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
}

func (s *FileStore) saveAll(m map[string]string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode secrets file: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create secrets dir: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write secrets temp file: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace secrets file: %w", err)
	}

	return nil
}
