package rtspproxy

import (
	"fmt"
	"sync"
	"time"
)

// StreamManager manages unique Stream instances.
type StreamManager struct {
	mu      sync.Mutex
	streams map[string]*Stream
	server  *Server
}

// NewStreamManager creates a new StreamManager.
func NewStreamManager(server *Server) *StreamManager {
	sm := &StreamManager{
		streams: make(map[string]*Stream),
		server:  server,
	}
	go sm.metricsLoop()
	return sm
}

func (sm *StreamManager) metricsLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-sm.server.ctx.Done():
			return
		case <-ticker.C:
			sm.mu.Lock()
			streams := make([]*Stream, 0, len(sm.streams))
			for _, s := range sm.streams {
				streams = append(streams, s)
			}
			sm.mu.Unlock()

			if len(streams) > 0 {
				Logf("📈 --- Global Proxy Metrics ---")
				Logf("Active Streams: %d", len(streams))
				for _, s := range streams {
					s.ReportMetrics()
				}
				Logf("-------------------------------")
			}
		}
	}
}

// GetStream returns an existing Stream or creates a new one for the given URL.
func (sm *StreamManager) GetStream(host, username, password, path string) *Stream {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Authentication context participates in stream identity to ensure isolation
	key := fmt.Sprintf("%s:%s@%s%s", username, password, host, path)
	if stream, ok := sm.streams[key]; ok {
		return stream
	}

	stream := NewStream(sm.server, host, username, password, path)
	// Set cleanup callback
	stream.onDestroy = func() {
		sm.RemoveStream(key)
		GlobalMetrics.ActiveStreams.Add(-1)
	}
	sm.streams[key] = stream
	GlobalMetrics.ActiveStreams.Add(1)
	return stream
}

// RemoveStream removes a stream from the manager.
func (sm *StreamManager) RemoveStream(key string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.streams, key)
}

// Shutdown stops all managed streams.
func (sm *StreamManager) Shutdown() {
	sm.mu.Lock()
	streams := make([]*Stream, 0, len(sm.streams))
	for _, s := range sm.streams {
		streams = append(streams, s)
	}
	sm.mu.Unlock()

	for _, s := range streams {
		s.Destroy()
	}
}
