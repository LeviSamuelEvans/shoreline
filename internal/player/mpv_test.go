package player

import (
	"encoding/json"
	"testing"
)

func TestEncodeCommand(t *testing.T) {
	data, err := encodeCommand("seek", 10, "relative")
	if err != nil {
		t.Fatalf("encodeCommand returned error: %v", err)
	}

	if got, want := data[len(data)-1], byte('\n'); got != want {
		t.Fatalf("command should end with newline, got %q", got)
	}

	var payload struct {
		Command []any `json:"command"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("command is not valid JSON: %v", err)
	}

	if got, want := len(payload.Command), 3; got != want {
		t.Fatalf("command length = %d, want %d", got, want)
	}
	if got, want := payload.Command[0], "seek"; got != want {
		t.Fatalf("command name = %q, want %q", got, want)
	}
	if got, want := payload.Command[1], float64(10); got != want {
		t.Fatalf("command arg = %v, want %v", got, want)
	}
	if got, want := payload.Command[2], "relative"; got != want {
		t.Fatalf("command mode = %q, want %q", got, want)
	}
}

func TestEncodeCommandRejectsEmptyName(t *testing.T) {
	if _, err := encodeCommand(""); err == nil {
		t.Fatal("encodeCommand should reject empty command names")
	}
}

func TestParsePropertyEvents(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		kind EventKind
	}{
		{
			name: "position",
			raw:  `{"event":"property-change","name":"time-pos","data":12.5}`,
			kind: EventPosition,
		},
		{
			name: "duration",
			raw:  `{"event":"property-change","name":"duration","data":180}`,
			kind: EventDuration,
		},
		{
			name: "pause",
			raw:  `{"event":"property-change","name":"pause","data":true}`,
			kind: EventPause,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event, ok := parseEvent([]byte(test.raw))
			if !ok {
				t.Fatal("parseEvent did not return an event")
			}
			if event.Kind != test.kind {
				t.Fatalf("event kind = %q, want %q", event.Kind, test.kind)
			}
		})
	}
}
