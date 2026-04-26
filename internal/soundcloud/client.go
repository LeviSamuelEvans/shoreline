package soundcloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/levievans/shoreline/internal/auth"
)

const baseURL = "https://api.soundcloud.com"

type Authenticator interface {
	AccessToken(ctx context.Context) (string, error)
}

type Client struct {
	authenticator Authenticator
	httpClient    *http.Client
}

type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	Permalink    string `json:"permalink"`
	PermalinkURL string `json:"permalink_url"`
}

type Track struct {
	ID               int64  `json:"id"`
	URN              string `json:"urn"`
	Title            string `json:"title"`
	PermalinkURL     string `json:"permalink_url"`
	StreamURL        string `json:"stream_url"`
	Duration         int64  `json:"duration"`
	PlaybackCount    int64  `json:"playback_count"`
	FavoritingsCount int64  `json:"favoritings_count"`
	Access           string `json:"access"`
	User             User   `json:"user"`
}

type Playlist struct {
	ID           int64   `json:"id"`
	Title        string  `json:"title"`
	PermalinkURL string  `json:"permalink_url"`
	TrackCount   int     `json:"track_count"`
	User         User    `json:"user"`
	Tracks       []Track `json:"tracks"`
}

type Activity struct {
	Type      string         `json:"type"`
	CreatedAt string         `json:"created_at"`
	Origin    ActivityOrigin `json:"origin"`
}

type ActivityOrigin struct {
	Track    *Track    `json:"track"`
	Playlist *Playlist `json:"playlist"`
	User     *User     `json:"user"`
}

type rawActivity struct {
	Type      string          `json:"type"`
	CreatedAt string          `json:"created_at"`
	Origin    json.RawMessage `json:"origin"`
}

type rawActivityOrigin struct {
	Track    *Track    `json:"track"`
	Playlist *Playlist `json:"playlist"`
	User     *User     `json:"user"`

	ID           int64  `json:"id"`
	URN          string `json:"urn"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	PermalinkURL string `json:"permalink_url"`
	StreamURL    string `json:"stream_url"`
	Duration     int64  `json:"duration"`
	TrackCount   int    `json:"track_count"`
}

type collectionResponse[T any] struct {
	Collection []T    `json:"collection"`
	NextHref   string `json:"next_href"`
}

func NewClient(authenticator Authenticator) *Client {
	return &Client{
		authenticator: authenticator,
		httpClient:    &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *Client) Me(ctx context.Context) (*User, error) {
	var user User
	if err := c.get(ctx, "/me", nil, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

func (c *Client) Likes(ctx context.Context, limit int) ([]Track, error) {
	params := url.Values{}
	params.Set("limit", fmt.Sprintf("%d", limit))
	params.Set("linked_partitioning", "true")

	var response collectionResponse[Track]
	if err := c.get(ctx, "/me/likes/tracks", params, &response); err != nil {
		return nil, err
	}
	return response.Collection, nil
}

func (c *Client) Playlists(ctx context.Context, limit int) ([]Playlist, error) {
	params := url.Values{}
	params.Set("limit", fmt.Sprintf("%d", limit))
	params.Set("linked_partitioning", "true")
	params.Set("show_tracks", "false")

	var response collectionResponse[Playlist]
	if err := c.get(ctx, "/me/playlists", params, &response); err != nil {
		return nil, err
	}
	return response.Collection, nil
}

func (c *Client) Feed(ctx context.Context, limit int) ([]Activity, error) {
	params := url.Values{}
	params.Set("limit", fmt.Sprintf("%d", limit))
	params.Set("linked_partitioning", "true")

	var response collectionResponse[rawActivity]
	if err := c.get(ctx, "/me/activities", params, &response); err != nil {
		return nil, err
	}

	activities := make([]Activity, 0, len(response.Collection))
	for _, item := range response.Collection {
		origin, err := normalizeActivityOrigin(item.Origin)
		if err != nil {
			return nil, err
		}
		activities = append(activities, Activity{
			Type:      item.Type,
			CreatedAt: item.CreatedAt,
			Origin:    origin,
		})
	}

	return activities, nil
}

func (c *Client) StreamURL(ctx context.Context, track Track) (string, error) {
	if track.URN != "" {
		url, err := c.resolveStreams(ctx, track.URN)
		if err == nil && url != "" {
			return url, nil
		}
	}

	if track.ID != 0 {
		url, err := c.resolveStreams(ctx, fmt.Sprintf("%d", track.ID))
		if err == nil && url != "" {
			return url, nil
		}
	}

	if track.StreamURL != "" {
		return c.resolvePlayableURL(ctx, track.StreamURL)
	}

	return "", fmt.Errorf("no playable stream found for track %q", track.Title)
}

func (c *Client) get(ctx context.Context, path string, params url.Values, dst any) error {
	token, err := c.authenticator.AccessToken(ctx)
	if err != nil {
		return err
	}

	endpoint := baseURL + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build GET %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "OAuth "+token)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("read %s response: %w", path, err)
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("%s failed: %s", path, res.Status)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}

	return nil
}

var _ Authenticator = (*auth.Manager)(nil)

func normalizeActivityOrigin(data json.RawMessage) (ActivityOrigin, error) {
	if len(data) == 0 {
		return ActivityOrigin{}, nil
	}

	var raw rawActivityOrigin
	if err := json.Unmarshal(data, &raw); err != nil {
		return ActivityOrigin{}, fmt.Errorf("decode activity origin: %w", err)
	}

	switch {
	case raw.Track != nil || raw.Playlist != nil:
		return ActivityOrigin{
			Track:    raw.Track,
			Playlist: raw.Playlist,
			User:     raw.User,
		}, nil
	case raw.Kind == "track" || raw.Title != "":
		return ActivityOrigin{
			Track: &Track{
				ID:           raw.ID,
				URN:          raw.URN,
				Title:        raw.Title,
				PermalinkURL: raw.PermalinkURL,
				Duration:     raw.Duration,
				StreamURL:    raw.StreamURL,
				User:         userOrZero(raw.User),
			},
			User: raw.User,
		}, nil
	case raw.Kind == "playlist":
		return ActivityOrigin{
			Playlist: &Playlist{
				ID:           raw.ID,
				Title:        raw.Title,
				PermalinkURL: raw.PermalinkURL,
				TrackCount:   raw.TrackCount,
				User:         userOrZero(raw.User),
			},
			User: raw.User,
		}, nil
	case raw.User != nil:
		return ActivityOrigin{
			User: raw.User,
		}, nil
	default:
		return ActivityOrigin{}, nil
	}
}

func userOrZero(user *User) User {
	if user == nil {
		return User{}
	}
	return *user
}

func (c *Client) resolveStreams(ctx context.Context, id string) (string, error) {
	token, err := c.authenticator.AccessToken(ctx)
	if err != nil {
		return "", err
	}

	endpoint := baseURL + "/tracks/" + url.PathEscape(id) + "/streams"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("build stream request: %w", err)
	}
	req.Header.Set("Accept", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "OAuth "+token)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("resolve streams: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read streams response: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return "", fmt.Errorf("stream lookup failed: %s", res.Status)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode streams response: %w", err)
	}

	for _, key := range []string{"http_mp3_128_url", "hls_mp3_128_url", "hls_aac_160_url"} {
		if value := streamField(payload, key); value != "" {
			return c.resolvePlayableURL(ctx, value)
		}
	}

	for _, value := range payload {
		if streamURL := streamValue(value); streamURL != "" {
			return c.resolvePlayableURL(ctx, streamURL)
		}
	}

	return "", fmt.Errorf("streams response did not contain a supported URL")
}

func (c *Client) resolvePlayableURL(ctx context.Context, endpoint string) (string, error) {
	token, err := c.authenticator.AccessToken(ctx)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("build playable stream request: %w", err)
	}
	req.Header.Set("Accept", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "OAuth "+token)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("resolve playable stream: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read playable stream response: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return "", fmt.Errorf("playable stream lookup failed: %s", res.Status)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		if value := streamField(payload, "url"); value != "" {
			return value, nil
		}
	}

	if res.Request != nil && res.Request.URL != nil && res.Request.URL.String() != endpoint {
		return res.Request.URL.String(), nil
	}

	if location := strings.TrimSpace(res.Header.Get("Location")); location != "" {
		return location, nil
	}

	raw := strings.TrimSpace(string(body))
	if strings.HasPrefix(raw, "#EXTM3U") {
		path, err := writePlaylist(raw)
		if err != nil {
			return "", err
		}
		return path, nil
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw, nil
	}

	return "", fmt.Errorf("playable stream response did not contain a final URL")
}

func writePlaylist(contents string) (string, error) {
	file, err := os.CreateTemp("", "shoreline-*.m3u8")
	if err != nil {
		return "", fmt.Errorf("create temp playlist: %w", err)
	}
	defer file.Close()

	if _, err := file.WriteString(contents); err != nil {
		return "", fmt.Errorf("write temp playlist: %w", err)
	}

	return file.Name(), nil
}

func streamField(payload map[string]any, key string) string {
	value, ok := payload[key]
	if !ok {
		return ""
	}
	return streamValue(value)
}

func streamValue(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		if raw, ok := v["url"].(string); ok {
			return strings.TrimSpace(raw)
		}
	}
	return ""
}
