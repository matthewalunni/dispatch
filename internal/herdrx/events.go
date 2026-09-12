package herdrx

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"time"
)

// EventKind is the class of runtime change herdr reported.
type EventKind string

const (
	// EventAgentStatusChanged is herdr's own lifecycle transition for an
	// agent: the signal dispatch would otherwise have to poll for.
	EventAgentStatusChanged EventKind = "agent_status_changed"
	// EventAgentDetected fires when herdr recognises an agent in a pane.
	EventAgentDetected EventKind = "agent_detected"
	// EventPaneClosed fires when a pane goes away.
	EventPaneClosed EventKind = "pane_closed"
	// EventPaneExited fires when a pane's process exits.
	EventPaneExited EventKind = "pane_exited"
	// EventStreamReady fires when a subscription is acknowledged, including
	// after a reconnect. Changes that happened while disconnected produce no
	// events, so a caller must treat this as "re-read everything".
	EventStreamReady EventKind = "stream_ready"
)

// Event is one runtime change relevant to a dispatched task.
type Event struct {
	Kind        EventKind
	PaneID      string
	WorkspaceID string
	Agent       string
	Status      Status
}

// ErrEventsUnsupported means live events are unavailable here and the caller
// should fall back to polling. It is not a failure worth surfacing to a user.
var ErrEventsUnsupported = errors.New("herdr live events are not available on this platform")

// Watcher streams herdr runtime events.
//
// herdr 0.9.0 exposes `events.subscribe` on its socket API but does not
// surface it through the CLI, so this speaks the newline-delimited JSON
// protocol directly. Everything else in this package still goes through the
// CLI, which is the interface herdr documents as stable.
type Watcher interface {
	// Watch streams events until ctx is cancelled or the connection drops.
	//
	// panes are the panes whose agent-status transitions matter; herdr
	// requires a pane id for that subscription. Pane lifecycle events are
	// subscribed globally, so a pane dispatch does not know about yet still
	// reports its own closure.
	Watch(ctx context.Context, panes []string, handle func(Event)) error
}

// socketRequest is one line of herdr's newline-delimited JSON API.
type socketRequest struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

type subscription struct {
	Type   string `json:"type"`
	PaneID string `json:"pane_id,omitempty"`
}

// eventEnvelope is what herdr pushes down a subscribed connection.
type eventEnvelope struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
	ID    string          `json:"id"`
	Error *Error          `json:"error"`
	// Result carries the subscription acknowledgement.
	Result json.RawMessage `json:"result"`
}

type eventData struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
	Agent       string `json:"agent"`
	AgentStatus Status `json:"agent_status"`
	Pane        *struct {
		PaneID      string `json:"pane_id"`
		WorkspaceID string `json:"workspace_id"`
		Agent       string `json:"agent"`
		AgentStatus Status `json:"agent_status"`
	} `json:"pane"`
}

// socketPath caches the resolved API socket so a reconnect does not have to
// shell out to `herdr status` again.
func (c *CLI) socketPath(ctx context.Context) (string, error) {
	c.socketOnce.Lock()
	defer c.socketOnce.Unlock()
	if c.cachedSocket != "" {
		return c.cachedSocket, nil
	}
	health := c.Health(ctx)
	if !health.Installed {
		return "", ErrNotInstalled
	}
	if health.Socket == "" {
		return "", errors.New("herdr did not report an API socket path")
	}
	c.cachedSocket = health.Socket
	return c.cachedSocket, nil
}

// Watch subscribes to herdr's live event stream.
func (c *CLI) Watch(ctx context.Context, panes []string, handle func(Event)) error {
	socket, err := c.socketPath(ctx)
	if err != nil {
		return err
	}
	// On Windows herdr's local socket is a named pipe, which Go's net package
	// will not dial. Callers fall back to polling rather than failing.
	if runtime.GOOS == "windows" || strings.HasPrefix(socket, `\\.\pipe\`) {
		return ErrEventsUnsupported
	}

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return fmt.Errorf("connect to herdr at %s: %w", socket, err)
	}
	defer conn.Close()

	// Closing the connection is what unblocks the read loop on cancellation.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	subs := []subscription{
		{Type: "pane.closed"},
		{Type: "pane.exited"},
		{Type: "pane.agent_detected"},
	}
	for _, pane := range panes {
		if pane != "" {
			subs = append(subs, subscription{Type: "pane.agent_status_changed", PaneID: pane})
		}
	}

	request, err := json.Marshal(socketRequest{
		ID:     "dispatch:events",
		Method: "events.subscribe",
		Params: map[string]any{"subscriptions": subs},
	})
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(request, '\n')); err != nil {
		return fmt.Errorf("subscribe to herdr events: %w", err)
	}

	reader := bufio.NewReaderSize(conn, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			switch decoded, kind := classifyLine(line); kind {
			case lineEvent:
				handle(decoded.Event)
			case lineReady:
				handle(Event{Kind: EventStreamReady})
			case lineError:
				return decoded.apiErr
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("herdr event stream ended: %w", err)
		}
	}
}

// lineKind classifies one line of the subscribed stream.
type lineKind int

const (
	lineIgnored lineKind = iota
	lineReady
	lineEvent
	lineError
)

// classified carries either a decoded event or the API error on that line.
type classified struct {
	Event
	apiErr error
}

func classifyLine(line []byte) (classified, lineKind) {
	var envelope eventEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		return classified{}, lineIgnored
	}
	if envelope.Error != nil {
		return classified{apiErr: envelope.Error}, lineError
	}
	if envelope.Event == "" {
		// The only non-event response on this connection is the subscription
		// acknowledgement.
		if len(envelope.Result) > 0 {
			return classified{}, lineReady
		}
		return classified{}, lineIgnored
	}
	event, ok := decodeEvent(line)
	if !ok {
		return classified{}, lineIgnored
	}
	return classified{Event: event}, lineEvent
}

// decodeEvent maps herdr's event envelope onto dispatch's own event type.
//
// herdr spells the same event two ways depending on the subscription
// ("pane.agent_status_changed" for the filtered one, "pane_closed" for the
// global ones), so both spellings are accepted.
func decodeEvent(line []byte) (Event, bool) {
	var envelope eventEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		return Event{}, false
	}
	if envelope.Event == "" {
		return Event{}, false
	}

	var data eventData
	if len(envelope.Data) > 0 {
		_ = json.Unmarshal(envelope.Data, &data)
	}
	if data.Pane != nil {
		if data.PaneID == "" {
			data.PaneID = data.Pane.PaneID
		}
		if data.WorkspaceID == "" {
			data.WorkspaceID = data.Pane.WorkspaceID
		}
		if data.Agent == "" {
			data.Agent = data.Pane.Agent
		}
		if data.AgentStatus == "" {
			data.AgentStatus = data.Pane.AgentStatus
		}
	}

	event := Event{
		PaneID:      data.PaneID,
		WorkspaceID: data.WorkspaceID,
		Agent:       data.Agent,
		Status:      data.AgentStatus,
	}

	switch normalizeEventName(envelope.Event) {
	case "pane_agent_status_changed":
		event.Kind = EventAgentStatusChanged
	case "pane_agent_detected":
		event.Kind = EventAgentDetected
	case "pane_closed":
		event.Kind = EventPaneClosed
	case "pane_exited":
		event.Kind = EventPaneExited
	default:
		return Event{}, false
	}
	return event, true
}

func normalizeEventName(name string) string {
	return strings.ReplaceAll(name, ".", "_")
}

// WatchWithRetry keeps a subscription alive across dropped connections and
// herdr restarts, calling handle for every event and onState whenever the
// stream connects or disconnects.
//
// It returns only when ctx is cancelled, or immediately when live events are
// not supported at all, so a caller can fall back to polling once rather than
// retrying something that can never work.
func WatchWithRetry(ctx context.Context, w Watcher, panes []string, handle func(Event), onState func(connected bool, err error)) error {
	const (
		minBackoff = 500 * time.Millisecond
		maxBackoff = 15 * time.Second
	)
	backoff := minBackoff

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		err := w.Watch(ctx, panes, func(event Event) {
			if event.Kind == EventStreamReady {
				backoff = minBackoff
				if onState != nil {
					onState(true, nil)
				}
			}
			handle(event)
		})

		if errors.Is(err, ErrEventsUnsupported) || errors.Is(err, ErrNotInstalled) {
			if onState != nil {
				onState(false, err)
			}
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if onState != nil {
			onState(false, err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

var _ Watcher = (*CLI)(nil)
