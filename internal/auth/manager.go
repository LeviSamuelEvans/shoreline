package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/levievans/shoreline/internal/config"
)

const (
	authorizeURL = "https://secure.soundcloud.com/authorize"
	tokenURL     = "https://secure.soundcloud.com/oauth/token"
	meURL        = "https://api.soundcloud.com/me"
)

type Manager struct {
	config     config.Config
	stateStore *config.StateStore
	secrets    *Secrets
	httpClient *http.Client
}

type LoginResult struct {
	RedirectURL string
	ExpiresAt   time.Time
	Username    string
}

type callbackResult struct {
	Code  string
	State string
	Err   error
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
}

type meResponse struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Permalink string `json:"permalink"`
}

func NewManager(cfg config.Config, stateStore *config.StateStore, secrets *Secrets) *Manager {
	return &Manager{
		config:     cfg,
		stateStore: stateStore,
		secrets:    secrets,
		httpClient: &http.Client{Timeout: 20 * time.Second},
	}
}

func (m *Manager) Login() (LoginResult, error) {
	state, err := m.stateStore.Load()
	if err != nil {
		return LoginResult{}, err
	}
	creds, err := m.secrets.LoadCredentials()
	if err != nil {
		return LoginResult{}, err
	}
	if err := m.secrets.SaveCredentials(creds); err != nil {
		return LoginResult{}, err
	}

	redirectURL := state.SoundCloud.RedirectURL
	if redirectURL == "" {
		return LoginResult{}, fmt.Errorf("missing redirect URL")
	}

	parsed, err := url.Parse(redirectURL)
	if err != nil {
		return LoginResult{}, fmt.Errorf("parse redirect URL: %w", err)
	}

	codeVerifier, err := randomURLSafe(64)
	if err != nil {
		return LoginResult{}, fmt.Errorf("generate code verifier: %w", err)
	}
	oauthState, err := randomURLSafe(32)
	if err != nil {
		return LoginResult{}, fmt.Errorf("generate state: %w", err)
	}

	callbackCh := make(chan callbackResult, 1)
	serverErrCh := make(chan error, 1)

	server := &http.Server{
		Addr:              parsed.Host,
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != parsed.Path {
				http.NotFound(w, r)
				return
			}

			if errText := r.URL.Query().Get("error"); errText != "" {
				callbackCh <- callbackResult{Err: fmt.Errorf("authorization failed: %s", errText)}
				http.Error(w, "authorization failed; you can close this tab", http.StatusBadRequest)
				return
			}

			callbackCh <- callbackResult{
				Code:  r.URL.Query().Get("code"),
				State: r.URL.Query().Get("state"),
			}

			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, "Shoreline authentication complete. You can return to the terminal.\n")
		}),
	}

	listener, err := net.Listen("tcp", parsed.Host)
	if err != nil {
		return LoginResult{}, fmt.Errorf("listen on callback address %q: %w", parsed.Host, err)
	}
	defer listener.Close()

	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			serverErrCh <- err
		}
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	authURL := buildAuthorizeURL(creds.ClientID, redirectURL, oauthState, codeChallenge(codeVerifier))
	if err := openBrowser(authURL); err != nil {
		fmt.Printf("open this URL to authenticate; do not share it:\n%s\n", authURL)
	}

	select {
	case callback := <-callbackCh:
		if callback.Err != nil {
			return LoginResult{}, callback.Err
		}
		if callback.State != oauthState {
			return LoginResult{}, fmt.Errorf("state mismatch in OAuth callback")
		}
		if callback.Code == "" {
			return LoginResult{}, fmt.Errorf("missing authorization code in callback")
		}

		token, err := m.exchangeCode(creds, state, callback.Code, codeVerifier)
		if err != nil {
			return LoginResult{}, err
		}

		account, err := m.fetchAccount(token.AccessToken)
		if err != nil {
			return LoginResult{}, err
		}

		state.SoundCloud.Account = account
		if err := m.secrets.SaveToken(token); err != nil {
			return LoginResult{}, err
		}
		if err := m.stateStore.Save(state); err != nil {
			return LoginResult{}, err
		}

		return LoginResult{
			RedirectURL: redirectURL,
			ExpiresAt:   token.ExpiresAt,
			Username:    account.Username,
		}, nil
	case err := <-serverErrCh:
		return LoginResult{}, fmt.Errorf("callback server failed: %w", err)
	case <-time.After(2 * time.Minute):
		return LoginResult{}, fmt.Errorf("timed out waiting for SoundCloud login callback")
	}
}

func (m *Manager) AccessToken(ctx context.Context) (string, error) {
	token, err := m.secrets.LoadToken()
	if err != nil {
		return "", err
	}
	if token == nil {
		return "", fmt.Errorf("not authenticated; run `sl login`")
	}
	if token.AccessToken == "" {
		return "", fmt.Errorf("not authenticated; run `sl login`")
	}
	if token.ExpiresAt.IsZero() || time.Until(token.ExpiresAt) > 30*time.Second {
		return token.AccessToken, nil
	}

	refreshed, err := m.refreshToken(ctx, token.RefreshToken)
	if err != nil {
		return "", err
	}
	if err := m.secrets.SaveToken(refreshed); err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

func buildAuthorizeURL(clientID, redirectURL, state, challenge string) string {
	values := url.Values{}
	values.Set("client_id", clientID)
	values.Set("redirect_uri", redirectURL)
	values.Set("response_type", "code")
	values.Set("code_challenge", challenge)
	values.Set("code_challenge_method", "S256")
	values.Set("state", state)
	return authorizeURL + "?" + values.Encode()
}

func codeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomURLSafe(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (m *Manager) exchangeCode(creds Credentials, state config.State, code, codeVerifier string) (*config.Token, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", creds.ClientID)
	form.Set("client_secret", creds.ClientSecret)
	form.Set("redirect_uri", state.SoundCloud.RedirectURL)
	form.Set("code_verifier", codeVerifier)
	form.Set("code", code)

	req, err := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Accept", "application/json; charset=utf-8")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exchange token: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("token exchange failed: %s", res.Status)
	}

	var payload tokenResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}

	return &config.Token{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		TokenType:    payload.TokenType,
		Scope:        payload.Scope,
		ExpiresAt:    time.Now().UTC().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}, nil
}

func (m *Manager) refreshToken(ctx context.Context, refreshToken string) (*config.Token, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("missing refresh token; run `sl login` again")
	}

	creds, err := m.secrets.LoadCredentials()
	if err != nil {
		return nil, err
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", creds.ClientID)
	form.Set("client_secret", creds.ClientSecret)
	form.Set("refresh_token", refreshToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Accept", "application/json; charset=utf-8")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read refresh response: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("token refresh failed: %s", res.Status)
	}

	var payload tokenResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode refresh response: %w", err)
	}

	return &config.Token{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		TokenType:    payload.TokenType,
		Scope:        payload.Scope,
		ExpiresAt:    time.Now().UTC().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}, nil
}

func (m *Manager) fetchAccount(accessToken string) (*config.Account, error) {
	req, err := http.NewRequest(http.MethodGet, meURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build /me request: %w", err)
	}
	req.Header.Set("Accept", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "OAuth "+accessToken)

	res, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch account: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read /me response: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("fetch account failed: %s", res.Status)
	}

	var payload meResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode /me response: %w", err)
	}

	return &config.Account{
		ID:        payload.ID,
		Username:  payload.Username,
		Permalink: payload.Permalink,
	}, nil
}

func openBrowser(target string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "linux":
		cmd = exec.Command("xdg-open", target)
	default:
		return fmt.Errorf("browser open is not supported on %s", runtime.GOOS)
	}

	return cmd.Start()
}
