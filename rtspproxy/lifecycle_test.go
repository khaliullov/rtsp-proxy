package rtspproxy

import (
	"context"
	"sync"
	"testing"
)

func TestStreamStateTransitions(t *testing.T) {
	s := NewStream(&Server{ctx: context.Background()}, "host", "user", "pass", "/path")

	// Initial state
	if s.GetState() != StateDisconnected {
		t.Errorf("expected Disdisconnected, got %s", s.GetState())
	}

	// Valid: Disconnected -> Connecting
	if err := s.transition(StateConnecting); err != nil {
		t.Errorf("expected success, got %v", err)
	}

	// Invalid: Connecting -> Disconnected
	if err := s.transition(StateDisconnected); err == nil {
		t.Error("expected error for Connecting -> Disconnected")
	}

	// Valid: Connecting -> Playing
	if err := s.transition(StatePlaying); err != nil {
		t.Errorf("expected success, got %v", err)
	}

	// Valid: Playing -> Reconnecting
	if err := s.transition(StateReconnecting); err != nil {
		t.Errorf("expected success, got %v", err)
	}

	// Valid: Reconnecting -> Playing
	if err := s.transition(StatePlaying); err != nil {
		t.Errorf("expected success, got %v", err)
	}

	// Destroy
	s.Destroy()
	if s.GetState() != StateDestroyed {
		t.Errorf("expected Destroyed, got %s", s.GetState())
	}

	// Invalid: Destroyed -> Anything
	if err := s.transition(StateConnecting); err == nil {
		t.Error("expected error for transition from Destroyed")
	}
}

func TestStreamManagerCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := &Server{ctx: ctx}
	sm := NewStreamManager(server)

	host, user, pass, path := "127.0.0.1", "admin", "123", "/stream"
	s := sm.GetStream(host, user, pass, path)

	sm.mu.Lock()
	if _, ok := sm.streams["admin:123@127.0.0.1/stream"]; !ok {
		sm.mu.Unlock()
		t.Fatal("stream not in manager")
	}
	sm.mu.Unlock()

	s.Destroy()

	// Stream should be removed from manager map
	sm.mu.Lock()
	_, ok := sm.streams["admin:123@127.0.0.1/stream"]
	sm.mu.Unlock()
	if ok {
		t.Error("stream still in manager after destruction")
	}
}

func TestStreamKeyingIsolation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := &Server{ctx: ctx}
	sm := NewStreamManager(server)

	host, path := "127.0.0.1", "/stream"
	s1 := sm.GetStream(host, "admin", "123", path)
	s2 := sm.GetStream(host, "hacker", "wrong", path)

	if s1 == s2 {
		t.Error("streams with different credentials should be isolated")
	}
}

func TestConcurrentSessionStartStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &Stream{
		remote: &Remote{Host: "localhost"},
		ctx:    ctx,
	}

	session := &Session{
		Timeout: 60,
		Stream:  s,
	}

	// Start/Stop in a tight loop from many goroutines
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ { // Reduce count slightly for race detector efficiency
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				session.StartUpstream()
				session.Stop()
			}
		}()
	}
	wg.Wait()
}

func TestResponseParsingExactLength(t *testing.T) {
	raw := "RTSP/1.0 200 OK\r\nContent-Length: 4\r\n\r\nBodyX"
	resp, err := NewResponseFromBuffer(raw)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Body != "Body" {
		t.Errorf("expected 'Body', got %q", resp.Body)
	}
}
