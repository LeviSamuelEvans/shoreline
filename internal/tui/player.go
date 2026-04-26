package tui

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/levievans/shoreline/internal/player"
	"github.com/levievans/shoreline/internal/soundcloud"
)

const (
	sourceLikes = iota
	sourceFeed
)

type TrackSource interface {
	Likes(ctx context.Context, limit int) ([]soundcloud.Track, error)
	Feed(ctx context.Context, limit int) ([]soundcloud.Activity, error)
	StreamURL(ctx context.Context, track soundcloud.Track) (string, error)
}

// Keep the UI behind narrow interfaces so most behavior can be tested without
// a terminal, a SoundCloud session, or a real mpv process.
type Player interface {
	Start(ctx context.Context) error
	Load(ctx context.Context, mediaURL string) error
	TogglePause(ctx context.Context) error
	Seek(ctx context.Context, seconds float64) error
	Stop(ctx context.Context) error
	Close() error
	Events() <-chan player.Event
}

type model struct {
	ctx context.Context

	source TrackSource
	player Player

	spinner  spinner.Model
	progress progress.Model

	width  int
	height int

	activeSource int
	selected     int
	likes        []soundcloud.Track
	feed         []soundcloud.Track

	queue        []soundcloud.Track
	currentIndex int
	currentTrack *soundcloud.Track

	loadingSources bool
	playerReady    bool
	resolving      bool
	paused         bool
	position       float64
	duration       float64
	status         string
	err            error
}

type sourcesLoadedMsg struct {
	likes []soundcloud.Track
	feed  []soundcloud.Track
	err   error
}

type playerStartedMsg struct {
	err error
}

type playerEventMsg struct {
	event player.Event
}

type streamResolvedMsg struct {
	index int
	track soundcloud.Track
	url   string
	err   error
}

type playerCommandMsg struct {
	err error
}

var (
	appStyle         = lipgloss.NewStyle().Padding(0, 1)
	panelStyle       = lipgloss.NewStyle().BorderStyle(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1)
	activeTabStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("4")).Padding(0, 1)
	inactiveTabStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Padding(0, 1)

	playingBadgeStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("10")).Padding(0, 1)
	pausedBadgeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("11")).Padding(0, 1)
	helpBarStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	trackSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("15")).
				Background(lipgloss.Color("4"))
	trackMutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
)

func Run(ctx context.Context, source TrackSource, mpv *player.MPV) error {
	if mpv == nil {
		return fmt.Errorf("mpv is not installed or not in PATH")
	}

	controller := mpv.NewController()
	defer controller.Close()

	program := tea.NewProgram(newModel(ctx, source, controller), tea.WithContext(ctx))
	_, err := program.Run()
	return err
}

func newModel(ctx context.Context, source TrackSource, player Player) model {
	spin := spinner.New(spinner.WithSpinner(spinner.Line))

	return model{
		ctx:            ctx,
		source:         source,
		player:         player,
		spinner:        spin,
		progress:       progress.New(progress.WithoutPercentage()),
		activeSource:   sourceLikes,
		currentIndex:   -1,
		loadingSources: true,
		status:         "Loading SoundCloud sources...",
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		// Starting mpv and loading SoundCloud can proceed independently; doing
		// both here keeps the first track selection from paying both costs.
		startPlayerCmd(m.ctx, m.player),
		loadSourcesCmd(m.ctx, m.source),
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.progress = progress.New(
			progress.WithoutPercentage(),
			progress.WithWidth(clamp(m.panelContentWidth(), 10, 72)),
		)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)
	case sourcesLoadedMsg:
		m.loadingSources = false
		if msg.err != nil {
			m.err = msg.err
			m.status = "Could not load SoundCloud sources."
			break
		}
		m.likes = msg.likes
		m.feed = msg.feed
		m.status = fmt.Sprintf("Loaded %d likes and %d feed tracks.", len(m.likes), len(m.feed))
	case playerStartedMsg:
		if msg.err != nil {
			m.err = msg.err
			m.status = "Could not start mpv."
			break
		}
		m.playerReady = true
		cmds = append(cmds, waitForPlayerEventCmd(m.player))
	case playerEventMsg:
		cmds = append(cmds, waitForPlayerEventCmd(m.player))
		var cmd tea.Cmd
		m, cmd = m.applyPlayerEvent(msg.event)
		cmds = append(cmds, cmd)
	case streamResolvedMsg:
		m.resolving = false
		if msg.err != nil {
			m.err = msg.err
			m.status = "Could not resolve stream."
			break
		}
		m.err = nil
		m.currentIndex = msg.index
		m.currentTrack = &msg.track
		m.position = 0
		m.duration = float64(msg.track.Duration) / 1000
		m.status = "Playing " + trackLabel(msg.track)
		cmds = append(cmds, loadPlayerCmd(m.ctx, m.player, msg.url))
	case playerCommandMsg:
		if msg.err != nil {
			m.err = msg.err
			m.status = "Player command failed."
		}
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	return m, tea.Batch(cmds...)
}

func (m model) View() tea.View {
	body := strings.Join([]string{
		m.headerView(),
		"",
		m.sourcesView(),
		"",
		m.nowPlayingView(),
		"",
		m.footerView(),
	}, "\n")
	if m.width == 0 || m.width >= 40 {
		body = appStyle.Render(body)
	}

	view := tea.NewView(body)
	// The player is a focused session, so the alternate screen keeps playback UI
	// from mixing with command output once the user quits.
	view.AltScreen = true
	return view
}

func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Sequence(stopPlayerCmd(m.ctx, m.player), func() tea.Msg { return tea.Quit() })
	case "tab":
		m.toggleSource()
	case "up", "k":
		m.moveSelection(-1)
	case "down", "j":
		m.moveSelection(1)
	case "enter":
		return m.playSelected()
	case " ", "space":
		if m.playerReady {
			return m, togglePauseCmd(m.ctx, m.player)
		}
	case "n":
		return m.playRelative(1)
	case "p":
		return m.playRelative(-1)
	case "left", "h":
		if m.playerReady {
			return m, seekCmd(m.ctx, m.player, -10)
		}
	case "right", "l":
		if m.playerReady {
			return m, seekCmd(m.ctx, m.player, 10)
		}
	}

	return m, nil
}

func (m model) headerView() string {
	status := m.status
	if m.loadingSources || m.resolving {
		status = m.spinner.View() + " " + status
	}

	width := m.bodyWidth()
	lines := []string{
		titleStyle.Render("shoreline player"),
		statusStyle.Render(truncateText(status, width)),
	}
	if m.err != nil {
		lines = append(lines, errorStyle.Render(truncateText(m.err.Error(), width)))
	}
	return strings.Join(lines, "\n")
}

func (m model) sourcesView() string {
	leftTitle := "Likes"
	rightTitle := "Feed"
	if m.activeSource == sourceLikes {
		leftTitle = activeTabStyle.Render(leftTitle)
	} else {
		leftTitle = inactiveTabStyle.Render(leftTitle)
	}
	if m.activeSource == sourceFeed {
		rightTitle = activeTabStyle.Render(rightTitle)
	} else {
		rightTitle = inactiveTabStyle.Render(rightTitle)
	}

	tabs := lipgloss.JoinHorizontal(lipgloss.Top, leftTitle, rightTitle)
	return tabs + "\n" + panelStyle.Width(m.panelContentWidth()).Render(m.trackListView())
}

func (m model) trackListView() string {
	tracks := m.activeTracks()
	width := m.panelContentWidth()
	sourceName := "Likes"
	if m.activeSource == sourceFeed {
		sourceName = "Feed"
	}
	lines := []string{
		trackMutedStyle.Render(truncateText(fmt.Sprintf("%s tracks (%d)", sourceName, len(tracks)), width)),
	}
	if len(tracks) == 0 {
		lines = append(lines, trackMutedStyle.Render(truncateText("No tracks loaded yet.", width)))
		return strings.Join(lines, "\n")
	}

	limit := min(len(tracks), m.trackListLimit())
	start := clamp(m.selected-limit/2, 0, max(0, len(tracks)-limit))
	for i := start; i < start+limit && i < len(tracks); i++ {
		prefix := "  "
		line := prefix + truncateText(trackLabel(tracks[i]), max(1, width-len(prefix)))
		if i == m.selected {
			line = trackSelectedStyle.Width(width).Render("> " + truncateText(trackLabel(tracks[i]), max(1, width-2)))
		}
		lines = append(lines, line)
	}
	if start+limit < len(tracks) {
		lines = append(lines, trackMutedStyle.Render(truncateText(fmt.Sprintf("+%d more", len(tracks)-start-limit), width)))
	}
	return strings.Join(lines, "\n")
}

func (m model) nowPlayingView() string {
	width := m.panelContentWidth()
	if m.currentTrack == nil {
		return panelStyle.Width(width).Render(trackMutedStyle.Render(truncateText("Nothing playing yet. Select a track and press enter.", width)))
	}

	badge := playingBadgeStyle.Render("PLAYING")
	if m.paused {
		badge = pausedBadgeStyle.Render("PAUSED")
	}
	percent := 0.0
	if m.duration > 0 {
		percent = clampFloat(m.position/m.duration, 0, 1)
	}
	title, artist := trackTitleArtist(*m.currentTrack)
	if artist == "" {
		artist = "Unknown artist"
	}

	card := strings.Join([]string{
		trackMutedStyle.Render("Now playing"),
		titleStyle.Render(truncateText(title, width)),
		trackMutedStyle.Render(truncateText(artist, width)),
		fmt.Sprintf("%s  %s / %s", badge, formatSeconds(m.position), formatSeconds(m.duration)),
		m.progress.ViewAs(percent),
		m.visualizerView(clamp(width, 8, 48)),
	}, "\n")
	return panelStyle.Width(width).Render(card)
}

func (m model) visualizerView(width int) string {
	if width <= 0 || m.currentTrack == nil {
		return ""
	}
	width = clamp(width, 1, 64)
	levels := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	b.Grow(width)
	for i := 0; i < width; i++ {
		phase := m.position*0.7 + float64(i)*0.58
		wave := (math.Sin(phase) + math.Sin(phase*0.43+1.8) + 2) / 4
		level := clamp(int(wave*float64(len(levels)-1)), 0, len(levels)-1)
		b.WriteRune(levels[level])
	}
	if m.paused {
		return trackMutedStyle.Render(b.String())
	}
	return statusStyle.Render(b.String())
}

func (m model) footerView() string {
	help := "tab source  enter play  space pause  n/p next/prev  left/right seek  q quit"
	if m.bodyWidth() < 64 {
		help = "tab source  enter play  space pause  n/p  h/l seek  q"
	}
	if m.bodyWidth() < 42 {
		help = "enter play  space pause  q quit"
	}
	return helpBarStyle.Render(truncateText(help, m.bodyWidth()))
}

func (m model) applyPlayerEvent(event player.Event) (model, tea.Cmd) {
	switch event.Kind {
	case player.EventPosition:
		m.position = event.Position
	case player.EventDuration:
		if event.Duration > 0 {
			m.duration = event.Duration
		}
	case player.EventPause:
		m.paused = event.Paused
	case player.EventIdle:
		if event.Idle && m.currentTrack != nil {
			m.status = "Playback idle."
		}
	case player.EventEndFile:
		if event.Reason == "eof" {
			next, cmd := m.playRelative(1)
			if nextModel, ok := next.(model); ok {
				m = nextModel
				m.status = "Advancing to next track..."
				return m, cmd
			}
		}
	case player.EventExit:
		if event.Err != nil {
			m.err = event.Err
		}
	case player.EventError:
		m.err = event.Err
	}
	return m, nil
}

func (m *model) toggleSource() {
	if m.activeSource == sourceLikes {
		m.activeSource = sourceFeed
	} else {
		m.activeSource = sourceLikes
	}
	m.selected = clamp(m.selected, 0, max(0, len(m.activeTracks())-1))
}

func (m *model) moveSelection(delta int) {
	tracks := m.activeTracks()
	if len(tracks) == 0 {
		m.selected = 0
		return
	}
	m.selected = clamp(m.selected+delta, 0, len(tracks)-1)
}

func (m model) playSelected() (tea.Model, tea.Cmd) {
	tracks := m.activeTracks()
	if len(tracks) == 0 {
		m.status = "No track selected."
		return m, nil
	}
	if !m.playerReady {
		m.status = "Waiting for mpv to start."
		return m, nil
	}

	// Snapshot the visible source into the queue so next/previous remains stable
	// even if the user switches tabs after playback starts.
	m.queue = tracks
	m.currentIndex = m.selected
	m.resolving = true
	m.status = "Resolving stream..."
	return m, resolveStreamCmd(m.ctx, m.source, tracks[m.selected], m.selected)
}

func (m model) playRelative(delta int) (tea.Model, tea.Cmd) {
	// If playback advances before a manual selection, use the active source as
	// the queue to keep key behavior predictable from the current screen.
	if len(m.queue) == 0 {
		m.queue = m.activeTracks()
		m.currentIndex = m.selected
	}
	if len(m.queue) == 0 {
		return m, nil
	}

	next := clamp(m.currentIndex+delta, 0, len(m.queue)-1)
	if next == m.currentIndex && m.currentTrack != nil {
		return m, nil
	}

	m.currentIndex = next
	m.resolving = true
	m.status = "Resolving stream..."
	return m, resolveStreamCmd(m.ctx, m.source, m.queue[next], next)
}

func (m model) activeTracks() []soundcloud.Track {
	if m.activeSource == sourceFeed {
		return m.feed
	}
	return m.likes
}

func loadSourcesCmd(ctx context.Context, source TrackSource) tea.Cmd {
	return func() tea.Msg {
		likes, err := source.Likes(ctx, 50)
		if err != nil {
			return sourcesLoadedMsg{err: err}
		}

		activities, err := source.Feed(ctx, 50)
		if err != nil {
			return sourcesLoadedMsg{err: err}
		}

		// The first player pass only queues actual tracks; feed entries for users
		// or playlists are still shown by `sl feed` but cannot play directly here.
		feed := make([]soundcloud.Track, 0, len(activities))
		for _, activity := range activities {
			if activity.Origin.Track != nil {
				feed = append(feed, *activity.Origin.Track)
			}
		}

		return sourcesLoadedMsg{likes: likes, feed: feed}
	}
}

func startPlayerCmd(ctx context.Context, player Player) tea.Cmd {
	return func() tea.Msg {
		return playerStartedMsg{err: player.Start(ctx)}
	}
}

func waitForPlayerEventCmd(player Player) tea.Cmd {
	return func() tea.Msg {
		event := <-player.Events()
		return playerEventMsg{event: event}
	}
}

func resolveStreamCmd(ctx context.Context, source TrackSource, track soundcloud.Track, index int) tea.Cmd {
	return func() tea.Msg {
		url, err := source.StreamURL(ctx, track)
		return streamResolvedMsg{index: index, track: track, url: url, err: err}
	}
}

func loadPlayerCmd(ctx context.Context, player Player, mediaURL string) tea.Cmd {
	return func() tea.Msg {
		return playerCommandMsg{err: player.Load(ctx, mediaURL)}
	}
}

func togglePauseCmd(ctx context.Context, player Player) tea.Cmd {
	return func() tea.Msg {
		return playerCommandMsg{err: player.TogglePause(ctx)}
	}
}

func seekCmd(ctx context.Context, player Player, seconds float64) tea.Cmd {
	return func() tea.Msg {
		return playerCommandMsg{err: player.Seek(ctx, seconds)}
	}
}

func stopPlayerCmd(ctx context.Context, player Player) tea.Cmd {
	return func() tea.Msg {
		return playerCommandMsg{err: player.Stop(ctx)}
	}
}

func trackLabel(track soundcloud.Track) string {
	if track.User.Username == "" {
		return track.Title
	}
	return track.User.Username + " - " + track.Title
}

func trackTitleArtist(track soundcloud.Track) (string, string) {
	return track.Title, track.User.Username
}

func formatSeconds(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	duration := time.Duration(seconds) * time.Second
	total := int(duration.Seconds())
	hours := total / 3600
	minutes := (total % 3600) / 60
	secs := total % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, secs)
	}
	return fmt.Sprintf("%d:%02d", minutes, secs)
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func clampFloat(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func (m model) bodyWidth() int {
	if m.width <= 0 {
		return 80
	}
	if m.width < 40 {
		return max(20, m.width)
	}
	return max(20, m.width-2)
}

func (m model) panelContentWidth() int {
	return max(16, m.bodyWidth()-4)
}

func (m model) trackListLimit() int {
	if m.height <= 0 {
		return 8
	}
	return clamp(m.height-16, 3, 14)
}

func truncateText(value string, width int) string {
	if width <= 0 || lipgloss.Width(value) <= width {
		return value
	}
	if width <= 3 {
		return strings.Repeat(".", width)
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes))+3 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "..."
}
