package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/levievans/shoreline/internal/auth"
	"github.com/levievans/shoreline/internal/config"
	"github.com/levievans/shoreline/internal/player"
	"github.com/levievans/shoreline/internal/soundcloud"
	"github.com/levievans/shoreline/internal/tui"
)

type Root struct {
	config config.Config
	state  *config.StateStore
	auth   *auth.Manager
	sc     *soundcloud.Client
	mpv    *player.MPV
	stdout io.Writer
	stderr io.Writer
}

func NewRoot(cfg config.Config, state *config.StateStore, authManager *auth.Manager, sc *soundcloud.Client, mpv *player.MPV) *Root {
	return &Root{
		config: cfg,
		state:  state,
		auth:   authManager,
		sc:     sc,
		mpv:    mpv,
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
}

func (r *Root) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		r.printHelp()
		return nil
	}

	switch strings.ToLower(args[0]) {
	case "-h", "--help", "help":
		r.printHelp()
		return nil
	case "login":
		return r.runLogin(ctx)
	case "feed":
		return r.runFeed(ctx)
	case "likes":
		return r.runLikes(ctx)
	case "playlists":
		return r.runPlaylists(ctx)
	case "play":
		return r.runPlay(ctx, args[1:])
	case "player":
		return r.runPlayer(ctx)
	case "stats":
		return r.runStats(ctx)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func (r *Root) printHelp() {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	section := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	command := lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	panel := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("12")).
		Padding(0, 1)

	var out strings.Builder
	fmt.Fprintln(&out, title.Render("shoreline"))
	fmt.Fprintln(&out, muted.Render("SoundCloud from your terminal."))
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, panel.Render(
		section.Render("Usage")+"\n"+
			"  "+command.Render("sl")+" "+accent.Render("<command>")+" "+muted.Render("[args]"),
	))
	fmt.Fprintln(&out)
	printHelpGroup(&out, section.Render("Browse"), []helpCommand{
		{"feed", "Show recent SoundCloud feed items"},
		{"likes", "Show liked tracks"},
		{"playlists", "Show your playlists"},
	}, command, muted)
	printHelpGroup(&out, section.Render("Playback"), []helpCommand{
		{"play likes <n>", "Play liked track number <n>"},
		{"play feed <n>", "Play feed track number <n>"},
		{"player", "Open the full-screen TUI player"},
	}, command, muted)
	printHelpGroup(&out, section.Render("Account"), []helpCommand{
		{"login", "Authenticate with SoundCloud"},
	}, command, muted)
	printHelpGroup(&out, section.Render("Other"), []helpCommand{
		{"stats", "Show local listening stats (coming soon)"},
		{"help", "Show this help"},
	}, command, muted)

	fmt.Fprintln(&out, section.Render("Examples"))
	for _, example := range []string{"sl login", "sl likes", "sl play likes 3", "sl player"} {
		fmt.Fprintf(&out, "  %s\n", command.Render(example))
	}

	fmt.Fprint(r.stdout, out.String())
}

type helpCommand struct {
	Name        string
	Description string
}

func printHelpGroup(out *strings.Builder, heading string, commands []helpCommand, commandStyle, mutedStyle lipgloss.Style) {
	fmt.Fprintln(out, heading)
	for _, cmd := range commands {
		fmt.Fprintf(out, "  %s  %s\n", commandStyle.Render(fmt.Sprintf("%-16s", cmd.Name)), mutedStyle.Render(cmd.Description))
	}
	fmt.Fprintln(out)
}

func (r *Root) runLogin(context.Context) error {
	result, err := r.auth.Login()
	if err != nil {
		return err
	}

	fmt.Fprintln(r.stdout, "authenticated with SoundCloud")
	fmt.Fprintf(r.stdout, "config dir: %s\n", r.config.Dir)
	fmt.Fprintf(r.stdout, "redirect url: %s\n", result.RedirectURL)
	fmt.Fprintf(r.stdout, "token expires at: %s\n", result.ExpiresAt.Format("2006-01-02 15:04:05 -0700"))
	if result.Username != "" {
		fmt.Fprintf(r.stdout, "account: %s\n", result.Username)
	}
	return nil
}

func (r *Root) runFeed(ctx context.Context) error {
	activities, err := r.sc.Feed(ctx, 20)
	if err != nil {
		return err
	}
	if len(activities) == 0 {
		fmt.Fprintln(r.stdout, "no feed items found")
		return nil
	}

	for i, item := range activities {
		fmt.Fprintf(r.stdout, "%2d. %s\n", i+1, formatActivity(item))
	}
	return nil
}

func (r *Root) runLikes(ctx context.Context) error {
	tracks, err := r.sc.Likes(ctx, 25)
	if err != nil {
		return err
	}
	if len(tracks) == 0 {
		fmt.Fprintln(r.stdout, "no liked tracks found")
		return nil
	}

	for i, track := range tracks {
		fmt.Fprintf(
			r.stdout,
			"%2d. %s - %s [%s]\n",
			i+1,
			track.User.Username,
			track.Title,
			soundcloud.FormatDuration(track.Duration),
		)
	}
	return nil
}

func (r *Root) runPlaylists(ctx context.Context) error {
	playlists, err := r.sc.Playlists(ctx, 25)
	if err != nil {
		return err
	}
	if len(playlists) == 0 {
		fmt.Fprintln(r.stdout, "no playlists found")
		return nil
	}

	for i, playlist := range playlists {
		fmt.Fprintf(
			r.stdout,
			"%2d. %s (%d tracks) - %s\n",
			i+1,
			playlist.Title,
			playlist.TrackCount,
			playlist.User.Username,
		)
	}
	return nil
}

func (r *Root) runPlayer(ctx context.Context) error {
	return tui.Run(ctx, r.sc, r.mpv)
}

func (r *Root) runStats(context.Context) error {
	fmt.Fprintln(r.stdout, "stats is not implemented yet")
	return nil
}

func (r *Root) runPlay(ctx context.Context, args []string) error {
	if r.mpv == nil {
		return fmt.Errorf("mpv is not installed or not in PATH")
	}
	if len(args) != 2 {
		return fmt.Errorf("usage: sl play <likes|feed> <index>")
	}

	index, err := strconv.Atoi(args[1])
	if err != nil || index < 1 {
		return fmt.Errorf("index must be a positive integer")
	}

	var track *soundcloud.Track

	switch strings.ToLower(args[0]) {
	case "likes":
		tracks, err := r.sc.Likes(ctx, 50)
		if err != nil {
			return err
		}
		if index > len(tracks) {
			return fmt.Errorf("index %d is out of range for likes", index)
		}
		track = &tracks[index-1]
	case "feed":
		activities, err := r.sc.Feed(ctx, 50)
		if err != nil {
			return err
		}
		if index > len(activities) {
			return fmt.Errorf("index %d is out of range for feed", index)
		}
		if activities[index-1].Origin.Track == nil {
			return fmt.Errorf("feed item %d is not a track", index)
		}
		track = activities[index-1].Origin.Track
	default:
		return fmt.Errorf("unknown play source %q; expected likes or feed", args[0])
	}

	streamURL, err := r.sc.StreamURL(ctx, *track)
	if err != nil {
		return err
	}

	fmt.Fprintf(r.stdout, "playing: %s - %s\n", track.User.Username, track.Title)
	return r.mpv.PlayURL(ctx, streamURL)
}

func formatActivity(item soundcloud.Activity) string {
	label := item.Type

	switch {
	case item.Origin.Track != nil:
		track := item.Origin.Track
		if track.User.Username != "" {
			return fmt.Sprintf("%-12s %s - %s", label, track.User.Username, track.Title)
		}
		return fmt.Sprintf("%-12s %s", label, track.Title)
	case item.Origin.Playlist != nil:
		playlist := item.Origin.Playlist
		if playlist.User.Username != "" {
			return fmt.Sprintf("%-12s %s - %s", label, playlist.User.Username, playlist.Title)
		}
		return fmt.Sprintf("%-12s %s", label, playlist.Title)
	case item.Origin.User != nil:
		return fmt.Sprintf("%-12s %s", label, item.Origin.User.Username)
	default:
		return label
	}
}
