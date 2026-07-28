package rtspproxy

import (
	"context"
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

	key := host + path
	if _, ok := sm.streams[key]; !ok {
		t.Fatal("stream not in manager")
	}

	s.Destroy()

	// Stream should be removed from manager map
	sm.mu.Lock()
	_, ok := sm.streams[key]
	sm.mu.Unlock()
	if ok {
		t.Error("stream still in manager after destruction")
	}
}
