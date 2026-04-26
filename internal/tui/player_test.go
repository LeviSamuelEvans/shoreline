package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/levievans/shoreline/internal/player"
	"github.com/levievans/shoreline/internal/soundcloud"
)

type fakeSource struct {
	streamURL string
}

func (s fakeSource) Likes(context.Context, int) ([]soundcloud.Track, error) {
	return []soundcloud.Track{testTrack(1), testTrack(2)}, nil
}

func (s fakeSource) Feed(context.Context, int) ([]soundcloud.Activity, error) {
	return []soundcloud.Activity{
		{Origin: soundcloud.ActivityOrigin{Track: ptrTrack(testTrack(3))}},
	}, nil
}

func (s fakeSource) StreamURL(context.Context, soundcloud.Track) (string, error) {
	return s.streamURL, nil
}

type fakePlayer struct {
	events       chan player.Event
	loaded       []string
	togglePauses int
}

func newFakePlayer() *fakePlayer {
	return &fakePlayer{events: make(chan player.Event, 4)}
}

func (p *fakePlayer) Start(context.Context) error { return nil }
func (p *fakePlayer) TogglePause(context.Context) error {
	p.togglePauses++
	return nil
}
func (p *fakePlayer) Seek(context.Context, float64) error { return nil }
func (p *fakePlayer) Stop(context.Context) error          { return nil }
func (p *fakePlayer) Close() error                        { return nil }
func (p *fakePlayer) Events() <-chan player.Event         { return p.events }
func (p *fakePlayer) Load(_ context.Context, mediaURL string) error {
	p.loaded = append(p.loaded, mediaURL)
	return nil
}

func TestSourcesLoadedSelectsLikesByDefault(t *testing.T) {
	m := newModel(context.Background(), fakeSource{}, newFakePlayer())

	updated, _ := m.Update(sourcesLoadedMsg{
		likes: []soundcloud.Track{testTrack(1), testTrack(2)},
		feed:  []soundcloud.Track{testTrack(3)},
	})

	got := updated.(model)
	if got.loadingSources {
		t.Fatal("loadingSources should be false after sources load")
	}
	if len(got.activeTracks()) != 2 {
		t.Fatalf("active likes length = %d, want 2", len(got.activeTracks()))
	}
}

func TestPlaySelectedBuildsQueueAndResolvesStream(t *testing.T) {
	m := newModel(context.Background(), fakeSource{streamURL: "https://example.test/stream"}, newFakePlayer())
	m.playerReady = true
	m.likes = []soundcloud.Track{testTrack(1), testTrack(2)}
	m.selected = 1

	updated, cmd := m.playSelected()
	got := updated.(model)
	if !got.resolving {
		t.Fatal("playSelected should mark the model as resolving")
	}
	if got.currentIndex != 1 {
		t.Fatalf("currentIndex = %d, want 1", got.currentIndex)
	}
	if len(got.queue) != 2 {
		t.Fatalf("queue length = %d, want 2", len(got.queue))
	}

	msg := cmd()
	resolved, ok := msg.(streamResolvedMsg)
	if !ok {
		t.Fatalf("command returned %T, want streamResolvedMsg", msg)
	}
	if resolved.url != "https://example.test/stream" {
		t.Fatalf("resolved URL = %q, want fake stream URL", resolved.url)
	}
}

func TestKeyNavigationMovesSelection(t *testing.T) {
	m := newModel(context.Background(), fakeSource{}, newFakePlayer())
	m.likes = []soundcloud.Track{testTrack(1), testTrack(2)}

	updated, _ := m.handleKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	got := updated.(model)
	if got.selected != 1 {
		t.Fatalf("selected = %d, want 1", got.selected)
	}
}

func TestSpaceTogglesPause(t *testing.T) {
	player := newFakePlayer()
	m := newModel(context.Background(), fakeSource{}, player)
	m.playerReady = true

	_, cmd := m.handleKey(tea.KeyPressMsg{Code: ' ', Text: " "})
	if cmd == nil {
		t.Fatal("space should return a pause command")
	}
	cmd()

	if player.togglePauses != 1 {
		t.Fatalf("toggle pauses = %d, want 1", player.togglePauses)
	}
}

func TestVisualizerViewIsStableForPosition(t *testing.T) {
	track := testTrack(1)
	m := newModel(context.Background(), fakeSource{}, newFakePlayer())
	m.currentTrack = &track
	m.position = 12.5

	got := m.visualizerView(24)
	if got != m.visualizerView(24) {
		t.Fatal("visualizer should be stable for the same model state")
	}
	if lipgloss.Width(got) != 24 {
		t.Fatalf("visualizer width = %d, want 24", lipgloss.Width(got))
	}
	for _, ch := range ".:-=+*#%@" {
		if strings.ContainsRune(got, ch) {
			t.Fatalf("visualizer should render as bars, found %q in %q", ch, got)
		}
	}

	m.position = 13.5
	if got == m.visualizerView(24) {
		t.Fatal("visualizer should change as playback position changes")
	}
}

func TestVisualizerViewIsStableWhilePaused(t *testing.T) {
	track := testTrack(1)
	m := newModel(context.Background(), fakeSource{}, newFakePlayer())
	m.currentTrack = &track
	m.position = 42
	m.paused = true

	got := m.visualizerView(16)
	if got != m.visualizerView(16) {
		t.Fatal("paused visualizer should be stable for the same model state")
	}
	if lipgloss.Width(got) != 16 {
		t.Fatalf("paused visualizer width = %d, want 16", lipgloss.Width(got))
	}
}

func testTrack(id int64) soundcloud.Track {
	return soundcloud.Track{
		ID:       id,
		Title:    "Track",
		Duration: 120000,
		User: soundcloud.User{
			Username: "artist",
		},
	}
}

func ptrTrack(track soundcloud.Track) *soundcloud.Track {
	return &track
}
