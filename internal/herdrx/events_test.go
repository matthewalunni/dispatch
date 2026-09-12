package herdrx

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeServer speaks herdr's newline-delimited JSON API over a unix socket.
type fakeServer struct {
	t        *testing.T
	path     string
	listener net.Listener

	mu            sync.Mutex
	subscriptions []map[string]any

	// pushes are sent to a client once it has subscribed.
	pushes []string
	// closeAfterPush drops the connection once everything has been sent.
	closeAfterPush bool
}

func newFakeServer(t *testing.T, pushes ...string) *fakeServer {
	t.Helper()
	// Unix socket paths are length-limited, so keep this short.
	dir, err := os.MkdirTemp("", "hx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	path := filepath.Join(dir, "s.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	s := &fakeServer{t: t, path: path, listener: listener, pushes: pushes}
	t.Cleanup(func() { listener.Close() })
	go s.serve()
	return s
}

func (s *fakeServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeServer) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}
	var request struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Params struct {
			Subscriptions []map[string]any `json:"subscriptions"`
		} `json:"params"`
	}
	if err := json.Unmarshal(line, &request); err != nil {
		return
	}
	s.mu.Lock()
	s.subscriptions = request.Params.Subscriptions
	s.mu.Unlock()

	if request.Method != "events.subscribe" {
		_, _ = conn.Write([]byte(`{"id":"x","error":{"code":"invalid_request","message":"unexpected method"}}` + "\n"))
		return
	}
	_, _ = conn.Write([]byte(`{"id":"` + request.ID + `","result":{"type":"subscription_started"}}` + "\n"))

	for _, push := range s.pushes {
		_, _ = conn.Write([]byte(push + "\n"))
	}
	if s.closeAfterPush {
		return
	}
	// Hold the connection open until the client goes away.
	_, _ = reader.ReadBytes('\n')
}

func (s *fakeServer) subs() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subscriptions
}

// watcherFor builds a CLI whose event socket points at the fake server.
func watcherFor(server *fakeServer) *CLI {
	c := NewCLI("herdr", "")
	c.cachedSocket = server.path
	return c
}

func collect(t *testing.T, c *CLI, panes []string, want int) ([]Event, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var events []Event
	err := c.Watch(ctx, panes, func(event Event) {
		events = append(events, event)
		if len(events) >= want {
			cancel()
		}
	})
	return events, err
}

func TestWatchSubscribesGloballyAndPerPane(t *testing.T) {
	server := newFakeServer(t)
	_, _ = collect(t, watcherFor(server), []string{"w1:p3", "w1:p4"}, 1)

	subs := server.subs()
	if len(subs) != 5 {
		t.Fatalf("subscriptions = %d, want 3 global + 2 per-pane: %v", len(subs), subs)
	}
	types := map[string]int{}
	panes := map[string]bool{}
	for _, sub := range subs {
		types[sub["type"].(string)]++
		if pane, ok := sub["pane_id"].(string); ok {
			panes[pane] = true
		}
	}
	for _, want := range []string{"pane.closed", "pane.exited", "pane.agent_detected"} {
		if types[want] != 1 {
			t.Errorf("missing global subscription %q: %v", want, types)
		}
	}
	// herdr requires a pane id for status transitions, so each watched pane
	// needs its own subscription.
	if types["pane.agent_status_changed"] != 2 {
		t.Errorf("per-pane status subscriptions = %d, want 2", types["pane.agent_status_changed"])
	}
	if !panes["w1:p3"] || !panes["w1:p4"] {
		t.Errorf("panes = %v", panes)
	}
}

func TestWatchEmitsReadyThenEvents(t *testing.T) {
	server := newFakeServer(t,
		`{"data":{"agent":"claude","agent_status":"working","pane_id":"w1:p3","workspace_id":"w1"},"event":"pane.agent_status_changed"}`,
		`{"data":{"agent":"claude","agent_status":"blocked","pane_id":"w1:p3","workspace_id":"w1"},"event":"pane.agent_status_changed"}`,
		`{"data":{"pane_id":"w1:p3","type":"pane_closed","workspace_id":"w1"},"event":"pane_closed"}`,
	)
	events, _ := collect(t, watcherFor(server), []string{"w1:p3"}, 4)

	if len(events) < 4 {
		t.Fatalf("events = %+v, want ready plus three events", events)
	}
	// A (re)connection must announce itself: anything that changed while
	// disconnected produced no event, so the caller has to re-read.
	if events[0].Kind != EventStreamReady {
		t.Errorf("first event = %q, want stream_ready", events[0].Kind)
	}
	if events[1].Kind != EventAgentStatusChanged || events[1].Status != StatusWorking || events[1].PaneID != "w1:p3" {
		t.Errorf("events[1] = %+v", events[1])
	}
	if events[2].Status != StatusBlocked {
		t.Errorf("events[2] = %+v", events[2])
	}
	if events[3].Kind != EventPaneClosed || events[3].PaneID != "w1:p3" {
		t.Errorf("events[3] = %+v", events[3])
	}
}

// herdr spells the same event two ways depending on the subscription.
func TestDecodeEventAcceptsBothSpellings(t *testing.T) {
	dotted := []byte(`{"data":{"agent_status":"blocked","pane_id":"w1:p1"},"event":"pane.agent_status_changed"}`)
	underscored := []byte(`{"data":{"pane_id":"w1:p1","type":"pane_exited"},"event":"pane_exited"}`)

	if event, ok := decodeEvent(dotted); !ok || event.Kind != EventAgentStatusChanged {
		t.Errorf("dotted form = %+v, ok=%v", event, ok)
	}
	if event, ok := decodeEvent(underscored); !ok || event.Kind != EventPaneExited {
		t.Errorf("underscored form = %+v, ok=%v", event, ok)
	}
}

// pane_updated nests the pane rather than flattening it.
func TestDecodeEventReadsNestedPaneInfo(t *testing.T) {
	line := []byte(`{"data":{"pane":{"agent_status":"idle","pane_id":"w1:pN","workspace_id":"w1","agent":"claude"},"type":"pane_agent_detected"},"event":"pane_agent_detected"}`)
	event, ok := decodeEvent(line)
	if !ok {
		t.Fatal("nested pane payload not decoded")
	}
	if event.PaneID != "w1:pN" || event.Agent != "claude" || event.Status != StatusIdle {
		t.Errorf("event = %+v", event)
	}
}

func TestDecodeEventIgnoresUnrelatedLines(t *testing.T) {
	for _, line := range []string{
		`{"id":"x","result":{"type":"subscription_started"}}`,
		`{"data":{},"event":"layout_updated"}`,
		`not json at all`,
		``,
	} {
		if _, ok := decodeEvent([]byte(line)); ok {
			t.Errorf("line %q should not decode as an event", line)
		}
	}
}

func TestWatchReturnsAPIErrors(t *testing.T) {
	dir, err := os.MkdirTemp("", "hx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = bufio.NewReader(conn).ReadBytes('\n')
		_, _ = conn.Write([]byte(`{"id":"x","error":{"code":"invalid_request","message":"missing field pane_id"}}` + "\n"))
	}()

	c := NewCLI("herdr", "")
	c.cachedSocket = path
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err = c.Watch(ctx, nil, func(Event) {})
	if !IsCode(err, "invalid_request") {
		t.Errorf("err = %v, want the API error surfaced", err)
	}
}

func TestWatchStopsWhenContextIsCancelled(t *testing.T) {
	server := newFakeServer(t)
	c := watcherFor(server)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Watch(ctx, []string{"w1:p1"}, func(Event) {}) }()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Watch did not return after its context was cancelled")
	}
}

func TestWatchOnAMissingSocket(t *testing.T) {
	c := NewCLI("herdr", "")
	c.cachedSocket = filepath.Join(t.TempDir(), "absent.sock")

	err := c.Watch(context.Background(), nil, func(Event) {})
	if err == nil {
		t.Fatal("expected a connection error")
	}
}

// stubWatcher drives WatchWithRetry without a socket.
type stubWatcher struct {
	mu       sync.Mutex
	attempts int
	err      error
	events   []Event
}

func (s *stubWatcher) Watch(ctx context.Context, _ []string, handle func(Event)) error {
	s.mu.Lock()
	s.attempts++
	attempt := s.attempts
	s.mu.Unlock()

	for _, event := range s.events {
		handle(event)
	}
	if s.err != nil {
		return s.err
	}
	if attempt >= 3 {
		<-ctx.Done()
		return ctx.Err()
	}
	return errors.New("connection dropped")
}

func TestWatchWithRetryReconnects(t *testing.T) {
	stub := &stubWatcher{events: []Event{{Kind: EventStreamReady}}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var ready int
	var connects int
	_ = WatchWithRetry(ctx, stub, nil,
		func(event Event) {
			if event.Kind == EventStreamReady {
				ready++
			}
		},
		func(connected bool, _ error) {
			if connected {
				connects++
			}
		})

	stub.mu.Lock()
	attempts := stub.attempts
	stub.mu.Unlock()
	if attempts < 3 {
		t.Errorf("attempts = %d, want the watcher to have reconnected", attempts)
	}
	// Every reconnect must re-announce readiness so the caller re-reads.
	if ready < 3 || connects < 3 {
		t.Errorf("ready = %d, connects = %d, want one per connection", ready, connects)
	}
}

func TestWatchWithRetryGivesUpWhenUnsupported(t *testing.T) {
	stub := &stubWatcher{err: ErrEventsUnsupported}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := WatchWithRetry(ctx, stub, nil, func(Event) {}, nil)
	if !errors.Is(err, ErrEventsUnsupported) {
		t.Errorf("err = %v, want ErrEventsUnsupported returned rather than retried", err)
	}
	stub.mu.Lock()
	attempts := stub.attempts
	stub.mu.Unlock()
	if attempts != 1 {
		t.Errorf("attempts = %d, want no retry of something that can never work", attempts)
	}
}
