package player

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type MPV struct {
	Path string
}

type EventKind string

const (
	EventPosition EventKind = "position"
	EventDuration EventKind = "duration"
	EventPause    EventKind = "pause"
	EventIdle     EventKind = "idle"
	EventEndFile  EventKind = "end-file"
	EventExit     EventKind = "exit"
	EventError    EventKind = "error"
)

type Event struct {
	Kind     EventKind
	Position float64
	Duration float64
	Paused   bool
	Idle     bool
	Reason   string
	Err      error
}

type Controller struct {
	path       string
	socketPath string
	cmd        *exec.Cmd
	conn       net.Conn
	events     chan Event
	mu         sync.Mutex
	closeOnce  sync.Once
}

func NewMPV() (*MPV, error) {
	path, err := exec.LookPath("mpv")
	if err != nil {
		return nil, fmt.Errorf("mpv is not installed or not in PATH")
	}
	return &MPV{Path: path}, nil
}

func (m *MPV) PlayURL(ctx context.Context, mediaURL string) error {
	cmd := exec.CommandContext(
		ctx,
		m.Path,
		"--no-video",
		"--force-window=no",
		mediaURL,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run mpv: %w", err)
	}

	return nil
}

func (m *MPV) NewController() *Controller {
	return &Controller{
		path:   m.Path,
		events: make(chan Event, 16),
	}
}

func (c *Controller) Start(ctx context.Context) error {
	if c.path == "" {
		return fmt.Errorf("mpv path is empty")
	}
	if c.cmd != nil {
		return nil
	}

	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("shoreline-mpv-%d.sock", time.Now().UnixNano()))
	cmd := exec.CommandContext(
		ctx,
		c.path,
		"--no-video",
		"--force-window=no",
		// Keep mpv alive between tracks so the TUI can own queue changes instead
		// of restarting the audio process for every selection
		"--idle=yes",
		// Terminal input belongs to Bubble Tea; mpv is controlled only over IPC
		"--input-terminal=no",
		"--really-quiet",
		"--input-ipc-server="+socketPath,
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start mpv: %w", err)
	}

	conn, err := dialSocket(ctx, socketPath)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = os.Remove(socketPath)
		return err
	}

	c.cmd = cmd
	c.conn = conn
	c.socketPath = socketPath

	go c.readEvents()
	go c.wait()

	// These properties are enough for the UI to render progress and state
	// without polling mpv or guessing from local timers
	for id, name := range []string{"time-pos", "duration", "pause", "idle-active"} {
		if err := c.sendCommand("observe_property", id+1, name); err != nil {
			return err
		}
	}

	return nil
}

func (c *Controller) Load(ctx context.Context, mediaURL string) error {
	if mediaURL == "" {
		return fmt.Errorf("media URL is empty")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.sendCommand("loadfile", mediaURL, "replace"); err != nil {
		return err
	}
	return c.Resume(ctx)
}

func (c *Controller) Pause(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.sendCommand("set_property", "pause", true)
}

func (c *Controller) Resume(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.sendCommand("set_property", "pause", false)
}

func (c *Controller) TogglePause(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.sendCommand("cycle", "pause")
}

func (c *Controller) Seek(ctx context.Context, seconds float64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.sendCommand("seek", seconds, "relative")
}

func (c *Controller) Stop(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.sendCommand("stop")
}

func (c *Controller) Close() error {
	var err error
	c.closeOnce.Do(func() {
		err = c.sendCommand("quit")
		if c.conn != nil {
			_ = c.conn.Close()
		}
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		if c.socketPath != "" {
			_ = os.Remove(c.socketPath)
		}
	})
	return err
}

func (c *Controller) Events() <-chan Event {
	return c.events
}

func (c *Controller) sendCommand(name string, args ...any) error {
	payload, err := encodeCommand(name, args...)
	if err != nil {
		return err
	}

	// mpv's IPC socket is a single JSON-lines stream; serialise writes so
	// concurrent TUI commands cannot interleave and corrupt a command.
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("mpv IPC is not connected")
	}
	if _, err := c.conn.Write(payload); err != nil {
		return fmt.Errorf("send mpv command %q: %w", name, err)
	}
	return nil
}

func (c *Controller) readEvents() {
	scanner := bufio.NewScanner(c.conn)
	for scanner.Scan() {
		event, ok := parseEvent(scanner.Bytes())
		if ok {
			c.emit(event)
		}
	}
	if err := scanner.Err(); err != nil {
		c.emit(Event{Kind: EventError, Err: fmt.Errorf("read mpv event: %w", err)})
	}
}

func (c *Controller) wait() {
	err := c.cmd.Wait()
	if c.socketPath != "" {
		_ = os.Remove(c.socketPath)
	}
	c.emit(Event{Kind: EventExit, Err: err})
	close(c.events)
}

func (c *Controller) emit(event Event) {
	select {
	case c.events <- event:
	default:
		// UI responsiveness matters more than preserving every progress tick
	}
}

func dialSocket(ctx context.Context, socketPath string) (net.Conn, error) {
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error

	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// mpv creates the IPC socket asynchronously after process start
		conn, err := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}

	return nil, fmt.Errorf("connect to mpv IPC socket: %w", lastErr)
}

func encodeCommand(name string, args ...any) ([]byte, error) {
	if name == "" {
		return nil, fmt.Errorf("mpv command name is empty")
	}

	payload := struct {
		Command []any `json:"command"`
	}{
		Command: append([]any{name}, args...),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode mpv command: %w", err)
	}
	return append(data, '\n'), nil
}

func parseEvent(data []byte) (Event, bool) {
	var payload struct {
		Event  string `json:"event"`
		Name   string `json:"name"`
		Data   any    `json:"data"`
		Reason string `json:"reason"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return Event{Kind: EventError, Err: fmt.Errorf("decode mpv event: %w", err)}, true
	}

	if payload.Error != "" && payload.Error != "success" {
		return Event{Kind: EventError, Err: fmt.Errorf("mpv command failed: %s", payload.Error)}, true
	}

	switch payload.Event {
	case "property-change":
		switch payload.Name {
		case "time-pos":
			return Event{Kind: EventPosition, Position: floatValue(payload.Data)}, true
		case "duration":
			return Event{Kind: EventDuration, Duration: floatValue(payload.Data)}, true
		case "pause":
			paused, _ := payload.Data.(bool)
			return Event{Kind: EventPause, Paused: paused}, true
		case "idle-active":
			idle, _ := payload.Data.(bool)
			return Event{Kind: EventIdle, Idle: idle}, true
		}
	case "end-file":
		return Event{Kind: EventEndFile, Reason: payload.Reason}, true
	}

	return Event{}, false
}

func floatValue(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	default:
		return 0
	}
}
