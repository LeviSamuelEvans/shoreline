package auth

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

const (
	keychainService = "shoreline"
	keyClientID     = "soundcloud_client_id"
	keyClientSecret = "soundcloud_client_secret"
	keyAccessToken  = "soundcloud_access_token"
	keyRefreshToken = "soundcloud_refresh_token"
	keyTokenType    = "soundcloud_token_type"
	keyTokenScope   = "soundcloud_token_scope"
	keyTokenExpiry  = "soundcloud_token_expiry"
)

type SecretStore interface {
	Get(name string) (string, error)
	Set(name, value string) error
	Delete(name string) error
}

type KeychainStore struct {
	service string
}

func NewKeychainStore() *KeychainStore {
	return &KeychainStore{service: keychainService}
}

func (s *KeychainStore) Get(name string) (string, error) {
	cmd := exec.Command(
		"security", "find-generic-password",
		"-s", s.service,
		"-a", name,
		"-w",
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		msg := stderr.String()
		if strings.Contains(msg, "could not be found") {
			return "", ErrSecretNotFound
		}
		return "", fmt.Errorf("read keychain item %q: %w", name, err)
	}

	return strings.TrimSpace(string(out)), nil
}

func (s *KeychainStore) Set(name, value string) error {
	cmd := exec.Command(
		"security", "add-generic-password",
		"-U",
		"-s", s.service,
		"-a", name,
		"-w", value,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write keychain item %q: %s", name, strings.TrimSpace(stderr.String()))
	}

	return nil
}

func (s *KeychainStore) Delete(name string) error {
	cmd := exec.Command(
		"security", "delete-generic-password",
		"-s", s.service,
		"-a", name,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		if strings.Contains(msg, "could not be found") {
			return ErrSecretNotFound
		}
		return fmt.Errorf("delete keychain item %q: %s", name, strings.TrimSpace(msg))
	}

	return nil
}
