package auth

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/levievans/shoreline/internal/config"
)

var ErrSecretNotFound = errors.New("secret not found")

type Secrets struct {
	store SecretStore
}

func NewSecrets(store SecretStore) *Secrets {
	return &Secrets{store: store}
}

type Credentials struct {
	ClientID     string
	ClientSecret string
}

func (s *Secrets) LoadCredentials() (Credentials, error) {
	clientID := os.Getenv("SHORELINE_SOUNDCLOUD_CLIENT_ID")
	clientSecret := os.Getenv("SHORELINE_SOUNDCLOUD_CLIENT_SECRET")

	if clientID == "" {
		value, err := s.store.Get(keyClientID)
		if err != nil && !errors.Is(err, ErrSecretNotFound) {
			return Credentials{}, err
		}
		clientID = value
	}

	if clientSecret == "" {
		value, err := s.store.Get(keyClientSecret)
		if err != nil && !errors.Is(err, ErrSecretNotFound) {
			return Credentials{}, err
		}
		clientSecret = value
	}

	if clientID == "" || clientSecret == "" {
		return Credentials{}, fmt.Errorf(
			"missing SoundCloud app credentials; set SHORELINE_SOUNDCLOUD_CLIENT_ID and SHORELINE_SOUNDCLOUD_CLIENT_SECRET or save them to Keychain",
		)
	}

	return Credentials{
		ClientID:     clientID,
		ClientSecret: clientSecret,
	}, nil
}

func (s *Secrets) SaveCredentials(creds Credentials) error {
	if creds.ClientID != "" {
		if err := s.store.Set(keyClientID, creds.ClientID); err != nil {
			return err
		}
	}
	if creds.ClientSecret != "" {
		if err := s.store.Set(keyClientSecret, creds.ClientSecret); err != nil {
			return err
		}
	}
	return nil
}

func (s *Secrets) LoadToken() (*config.Token, error) {
	accessToken, err := s.store.Get(keyAccessToken)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return nil, nil
		}
		return nil, err
	}

	refreshToken, err := s.optionalGet(keyRefreshToken)
	if err != nil {
		return nil, err
	}
	tokenType, err := s.optionalGet(keyTokenType)
	if err != nil {
		return nil, err
	}
	scope, err := s.optionalGet(keyTokenScope)
	if err != nil {
		return nil, err
	}
	expiresAtRaw, err := s.optionalGet(keyTokenExpiry)
	if err != nil {
		return nil, err
	}

	var expiresAt time.Time
	if expiresAtRaw != "" {
		expiresAt, err = time.Parse(time.RFC3339, expiresAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse token expiry from keychain: %w", err)
		}
	}

	return &config.Token{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		TokenType:    tokenType,
		Scope:        scope,
		ExpiresAt:    expiresAt,
	}, nil
}

func (s *Secrets) SaveToken(token *config.Token) error {
	if token == nil {
		return nil
	}

	if err := s.store.Set(keyAccessToken, token.AccessToken); err != nil {
		return err
	}
	if token.RefreshToken != "" {
		if err := s.store.Set(keyRefreshToken, token.RefreshToken); err != nil {
			return err
		}
	}
	if token.TokenType != "" {
		if err := s.store.Set(keyTokenType, token.TokenType); err != nil {
			return err
		}
	}
	if token.Scope != "" {
		if err := s.store.Set(keyTokenScope, token.Scope); err != nil {
			return err
		}
	}
	if !token.ExpiresAt.IsZero() {
		if err := s.store.Set(keyTokenExpiry, token.ExpiresAt.UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}

	return nil
}

func (s *Secrets) optionalGet(name string) (string, error) {
	value, err := s.store.Get(name)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return "", nil
		}
		return "", err
	}
	return value, nil
}
