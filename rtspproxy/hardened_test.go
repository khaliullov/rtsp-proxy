package rtspproxy

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestShutdownDuringHandshake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := NewServer(ctx)

	// Mock camera server that is slow
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	camAddr := ln.Addr().String()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Sleep during handshake
		time.Sleep(2 * time.Second)
	}()

	stream := server.LookupStream(camAddr, "user", "pass", "/slow")
	go stream.Start()

	// Shutdown server while stream is connecting
	time.Sleep(500 * time.Millisecond)
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Errorf("shutdown failed: %v", err)
	}
}

func TestCredentialHijackPrevention(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(ctx)
	sm := server.streamManager

	host, path := "127.0.0.1", "/stream"

	// Create stream with valid credentials
	s1 := sm.GetStream(host, "admin", "secret", path)

	// Try to get same stream with WRONG credentials
	s2 := sm.GetStream(host, "admin", "wrong", path)

	if s1 == s2 {
		t.Fatal("security breach: streams with different credentials shared same object")
	}
}

func TestRapidReconnectLifecycleStress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(ctx)

	// Start a mock camera
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	camAddr := ln.Addr().String()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				// Send some data then close randomly
				c.Write([]byte("RTSP/1.0 200 OK\r\nCSeq: 1\r\n\r\n"))
				time.Sleep(100 * time.Millisecond)
			}(conn)
		}
	}()

	stream := server.LookupStream(camAddr, "a", "b", "/stress")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				stream.Start()
				time.Sleep(50 * time.Millisecond)
				stream.Stop()
			}
		}()
	}
	wg.Wait()
	ln.Close()
}

func TestDisconnectDeadlock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(ctx)

	// Mock camera server
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	camAddr := ln.Addr().String()

	stream := server.LookupStream(camAddr, "a", "b", "/deadlock")

	// Hold the stream lock and call Disconnect
	// This previously would deadlock because Disconnect tries to lock stream.mu
	stream.mu.Lock()
	remote := NewRemote(stream)
	stream.remote = remote
	remote.RemoteConn = &net.TCPConn{} // dummy

	done := make(chan struct{})
	go func() {
		remote.Disconnect()
		close(done)
	}()

	select {
	case <-done:
		// Success, no deadlock
	case <-time.After(1 * time.Second):
		t.Fatal("Deadlock detected in Remote.Disconnect!")
	}
	stream.mu.Unlock()
}

func TestMetricsConsistency(t *testing.T) {
	s := NewStream(&Server{ctx: context.Background()}, "host", "user", "pass", "/path")
	s.StartTime = time.Now().Add(-10 * time.Second)

	// Valid sequence: Disconnected -> Connecting -> Playing
	s.transition(StateConnecting)
	s.transition(StatePlaying)

	s.SessionStartTime = time.Now().Add(-2 * time.Second)
	atomic.AddUint64(&s.BytesForwarded, 1000)
	atomic.AddUint64(&s.SessionBytesForwarded, 200)

	br := s.GetBitrate()
	if br == 0 {
		t.Error("bitrate should not be zero")
	}

	// Simulate reconnect: Playing -> Reconnecting -> Connecting -> Playing
	s.transition(StateReconnecting)
	s.transition(StateConnecting)
	s.transition(StatePlaying)

	if atomic.LoadUint64(&s.SessionBytesForwarded) != 0 {
		t.Errorf("SessionBytesForwarded should be reset on Playing, got %d", atomic.LoadUint64(&s.SessionBytesForwarded))
	}
}

func TestTeardownSequence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(ctx)

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	camAddr := ln.Addr().String()

	teardownReceived := make(chan struct{})

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1024)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			if strings.Contains(string(buf[:n]), "TEARDOWN") {
				close(teardownReceived)
				conn.Write([]byte("RTSP/1.0 200 OK\r\nCSeq: 1\r\n\r\n"))
				return
			}
			conn.Write([]byte("RTSP/1.0 200 OK\r\nCSeq: 1\r\n\r\n"))
		}
	}()

	stream := server.LookupStream(camAddr, "a", "b", "/teardown")
	stream.Start()
	time.Sleep(500 * time.Millisecond)

	// Register a session so Stop() has something to teardown
	stream.mu.Lock()
	stream.sessions["12345"] = NewSession(stream, "12345", 60)
	stream.mu.Unlock()

	stream.Stop()

	select {
	case <-teardownReceived:
		// Success
	case <-time.After(2 * time.Second):
		t.Error("TEARDOWN not sent by proxy")
	}
}

func TestOrderedForwarding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(ctx)

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	camAddr := ln.Addr().String()

	received := make([]int, 0)
	var mu sync.Mutex

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Minimal handshake
		buf := make([]byte, 1024)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			req := string(buf[:n])
			if strings.Contains(req, "OPTIONS") {
				conn.Write([]byte("RTSP/1.0 200 OK\r\nPublic: OPTIONS, DESCRIBE, SETUP, PLAY\r\nCSeq: 1\r\n\r\n"))
			} else if strings.Contains(req, "DESCRIBE") {
				sdp := "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=Mock\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=video 0 RTP/AVP 96\r\na=control:track1\r\n"
				conn.Write([]byte(fmt.Sprintf("RTSP/1.0 200 OK\r\nContent-Type: application/sdp\r\nContent-Length: %d\r\nCSeq: 2\r\n\r\n%s", len(sdp), sdp)))
			} else if strings.Contains(req, "SETUP") {
				conn.Write([]byte("RTSP/1.0 200 OK\r\nTransport: RTP/AVP/TCP;unicast;interleaved=0-1;ssrc=12345678\r\nSession: 1234\r\nCSeq: 3\r\n\r\n"))
			} else if strings.Contains(req, "PLAY") {
				conn.Write([]byte("RTSP/1.0 200 OK\r\nSession: 1234\r\nCSeq: 4\r\n\r\n"))
				break // Handshake done
			}
		}

		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			data := buf[:n]
			for i := 0; i < len(data); {
				if data[i] == '$' {
					ch := int(data[i+1])
					mu.Lock()
					received = append(received, ch)
					mu.Unlock()
					i += 4 + ((int(data[i+2]) << 8) | int(data[i+3]))
				} else {
					i++
				}
			}
		}
	}()

	stream := server.LookupStream(camAddr, "a", "b", "/ordered")
	stream.Start()

	// Wait for stream to be connected
	select {
	case <-stream.ReadyCh():
	case <-time.After(5 * time.Second):
		t.Fatal("stream failed to connect")
	}

	// Simulate client sending interleaved data
	stream.mu.RLock()
	remote := stream.remote
	stream.mu.RUnlock()

	if remote == nil {
		t.Fatal("remote not started")
	}

	for i := 0; i < 100; i++ {
		remote.SendBinary(i%10, []byte{1, 2, 3})
	}

	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 100 {
		t.Errorf("expected 100 packets, got %d", len(received))
	}
	for i, ch := range received {
		if ch != i%10 {
			t.Errorf("out of order at %d: expected %d, got %d", i, i%10, ch)
		}
	}
}
