package rtspproxy

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// StreamState represents the current state of the stream.
type StreamState int

const (
	StateDisconnected StreamState = iota
	StateConnecting
	StateReady
	StatePlaying
	StateReconnecting
	StateStopping
	StateDestroyed
)

func (s StreamState) String() string {
	switch s {
	case StateDisconnected:
		return "Disconnected"
	case StateConnecting:
		return "Connecting"
	case StateReady:
		return "Ready"
	case StatePlaying:
		return "Playing"
	case StateReconnecting:
		return "Reconnecting"
	case StateStopping:
		return "Stopping"
	case StateDestroyed:
		return "Destroyed"
	default:
		return "Unknown"
	}
}

// Stream represents an RTSP stream from a remote server.
type Stream struct {
	mu sync.RWMutex

	Host     string
	Username string
	Password string
	Path     string

	SDP     string
	Options string
	Server  string

	state     StreamState
	remote    *Remote
	server    *Server
	onDestroy func()

	clients     map[*Client]*ClientSession
	sessions    map[string]*Session
	lastClient  time.Time
	idleTimer   *time.Timer
	loopStarted atomic.Bool

	// Metrics
	PacketsForwarded uint64
	PacketsDropped   uint64
	BytesForwarded   uint64
	ReconnectCount   uint64
	LastReconnect    time.Time
	ReconnectTotal   time.Duration
	StartTime        time.Time
	LastPktTime      time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	IdleTimeout time.Duration
}

// NewStream creates a new Stream instance.
func NewStream(server *Server, host, username, password, path string) *Stream {
	ctx, cancel := context.WithCancel(server.ctx)
	s := &Stream{
		Host:        host,
		Username:    username,
		Password:    password,
		Path:        path,
		server:      server,
		state:       StateDisconnected,
		clients:     make(map[*Client]*ClientSession),
		sessions:    make(map[string]*Session),
		ctx:         ctx,
		cancel:      cancel,
		IdleTimeout: GlobalConfig.IdleTimeout,
		StartTime:   time.Now(),
	}
	return s
}

// GetState returns the current state of the stream.
func (s *Stream) GetState() StreamState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *Stream) setState(state StreamState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unsafeSetState(state)
}

func (s *Stream) unsafeSetState(state StreamState) {
	if s.state == StateDestroyed {
		return
	}
	if s.state == state {
		return
	}
	Logf("Stream [%s] state change: %s -> %s", s.Path, s.state, state)
	s.state = state
}

func (s *Stream) transition(to StreamState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == StateDestroyed {
		return fmt.Errorf("stream is destroyed")
	}

	valid := false
	switch s.state {
	case StateDisconnected:
		valid = to == StateConnecting || to == StateDestroyed
	case StateConnecting:
		valid = to == StatePlaying || to == StateReconnecting || to == StateStopping || to == StateDestroyed
	case StatePlaying:
		valid = to == StateReconnecting || to == StateStopping || to == StateDestroyed
	case StateReconnecting:
		valid = to == StatePlaying || to == StateDisconnected || to == StateStopping || to == StateDestroyed
	case StateStopping:
		valid = to == StateDisconnected || to == StateDestroyed
	}

	if !valid {
		return fmt.Errorf("invalid state transition: %s -> %s", s.state, to)
	}

	s.unsafeSetState(to)
	return nil
}

// AddClient registers a client to the stream.
func (s *Stream) AddClient(client *Client, sessionID string) *ClientSession {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stopIdleTimer()

	if cs, ok := s.clients[client]; ok {
		return cs
	}

	cs := NewClientSession(client, s, sessionID)
	s.clients[client] = cs
	cs.Start()

	// If we were disconnected, start connecting
	if s.state == StateDisconnected {
		s.startConnectLoop()
	}

	return cs
}

// RemoveClient unregisters a client from the stream.
func (s *Stream) RemoveClient(client *Client) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cs, ok := s.clients[client]; ok {
		cs.Stop()
		delete(s.clients, client)
	}

	if len(s.clients) == 0 && s.state != StateDestroyed {
		s.lastClient = time.Now()
		s.resetIdleTimer()
	}
}

// Start initiates the upstream connection if not already started.
func (s *Stream) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != StateDisconnected {
		return
	}

	if len(s.clients) == 0 {
		s.resetIdleTimer()
	}

	s.startConnectLoop()
}

func (s *Stream) startConnectLoop() {
	if s.loopStarted.Swap(true) {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.loopStarted.Store(false)
		s.connectLoop()
	}()
}

func (s *Stream) resetIdleTimer() {
	if s.idleTimer == nil {
		s.idleTimer = time.AfterFunc(s.IdleTimeout, func() {
			s.mu.Lock()
			if len(s.clients) == 0 && s.state != StateDestroyed {
				Logf("Stream [%s] idle for %v, stopping.", s.Path, s.IdleTimeout)
				s.mu.Unlock()
				s.Stop()
			} else {
				s.mu.Unlock()
			}
		})
	} else {
		s.idleTimer.Reset(s.IdleTimeout)
	}
}

func (s *Stream) stopIdleTimer() {
	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}
}

// Stop closes the upstream connection.
func (s *Stream) Stop() {
	if err := s.transition(StateStopping); err != nil {
		return
	}
	s.mu.Lock()
	if s.remote != nil {
		s.remote.Disconnect()
	}
	s.mu.Unlock()
	s.transition(StateDisconnected)
}

// Destroy cleans up all resources.
func (s *Stream) Destroy() {
	s.transition(StateDestroyed)
	s.cancel()
	s.mu.Lock()
	if s.remote != nil {
		s.remote.Disconnect()
	}
	for _, cs := range s.clients {
		cs.Stop()
	}
	s.clients = make(map[*Client]*ClientSession)
	s.stopIdleTimer()
	s.mu.Unlock()

	if s.onDestroy != nil {
		s.onDestroy()
	}
	s.wg.Wait()
}

func (s *Stream) connectLoop() {
	backoff := GlobalConfig.ReconnectBackoff
	idx := 0

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		s.mu.RLock()
		st := s.state
		s.mu.RUnlock()

		if st == StateStopping || st == StateDestroyed {
			return
		}

		if st == StateReconnecting {
			atomic.AddUint64(&s.ReconnectCount, 1)
		} else {
			if err := s.transition(StateConnecting); err != nil {
				return
			}
		}

		s.mu.Lock()
		if s.remote != nil {
			s.remote.Disconnect()
		}
		s.remote = NewRemote(s.server, s.Host, s.Username, s.Password)
		remote := s.remote
		s.mu.Unlock()

		if remote == nil {
			LogCriticalf("Stream [%s] failed to create remote", s.Path)
			s.transition(StateDisconnected)
			return
		}

		err := remote.Dial()
		if err == nil {
			readCtx, readCancel := context.WithCancel(s.ctx)
			readDone := make(chan error, 1)
			go func() {
				readDone <- s.readLoop(readCtx)
			}()

			err = s.doConnectSequence()
			if err == nil {
				if err := s.transition(StatePlaying); err != nil {
					readCancel()
					<-readDone
					return
				}
				idx = 0 // Reset backoff

				// Wait for read loop to finish (or error)
				err = <-readDone
				readCancel()

				s.mu.RLock()
				st = s.state
				s.mu.RUnlock()

				if st == StateStopping || st == StateDestroyed {
					s.transition(StateDisconnected)
					return
				}

				if err != nil {
					LogCriticalf("Stream [%s] read error: %v", s.Path, err)
					s.transition(StateReconnecting)
				} else {
					// Clean shutdown or idle
					s.transition(StateDisconnected)
					return
				}
			} else {
				readCancel()
				<-readDone

				s.mu.RLock()
				numClients := len(s.clients)
				s.mu.RUnlock()

				// If reconnect failed and no clients, stop trying to avoid infinite logs
				if numClients == 0 {
					LogCriticalf("Stream [%s] connect failed and no clients, stopping retries.", s.Path)
					s.transition(StateDisconnected)
					return
				}

				LogCriticalf("Stream [%s] connect sequence error: %v. Retrying in %v...", s.Path, err, backoff[idx])
				s.transition(StateReconnecting)
			}
		} else {
			s.mu.RLock()
			numClients := len(s.clients)
			s.mu.RUnlock()

			if numClients == 0 {
				LogCriticalf("Stream [%s] dial failed and no clients, stopping retries.", s.Path)
				s.transition(StateDisconnected)
				return
			}

			LogCriticalf("Stream [%s] dial error: %v. Retrying in %v...", s.Path, err, backoff[idx])
			s.transition(StateReconnecting)
		}

		select {
		case <-s.ctx.Done():
			return
		case <-time.After(backoff[idx]):
			idx++
			if idx >= len(backoff) {
				idx = len(backoff) - 1
			}
		}
	}
}

func (s *Stream) doConnectSequence() error {
	remote := s.remote

	// 1. OPTIONS
	_, err := remote.GetOptions(s.Path)
	if err != nil {
		return fmt.Errorf("OPTIONS failed: %w", err)
	}

	remote.connMutex.Lock()
	if rs, ok := remote.streams[s.Path]; ok {
		s.mu.Lock()
		s.Options = rs.Options
		s.Server = rs.Server
		s.mu.Unlock()
	}
	remote.connMutex.Unlock()

	// 2. DESCRIBE
	sdp, err := remote.GetSDP(s.Path)
	if err != nil {
		return fmt.Errorf("DESCRIBE failed: %w", err)
	}
	s.mu.Lock()
	s.SDP = sdp
	s.mu.Unlock()

	// 3. SETUP (for each track in SDP)
	tracks := s.parseTracks(sdp)
	if len(tracks) == 0 {
		return fmt.Errorf("no tracks found in SDP")
	}

	sessionID := ""
	for i, track := range tracks {
		transportStr := fmt.Sprintf("RTP/AVP/TCP;unicast;interleaved=%d-%d", i*2, i*2+1)
		ssrc, sess, err := remote.SetupUpstream(s, track, transportStr)
		if err != nil {
			return fmt.Errorf("SETUP failed for track %s: %w", track, err)
		}
		sessionID = sess
		Logf("Stream [%s] track %s setup with SSRC %s", s.Path, track, ssrc)
	}

	// 4. PLAY
	_, err = remote.PlayUpstream(s.Path, sessionID)
	if err != nil {
		return fmt.Errorf("PLAY failed: %w", err)
	}

	return nil
}

// GetSDP returns the current SDP description.
func (s *Stream) GetSDP() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.SDP
}

// GetOptions returns the current OPTIONS response.
func (s *Stream) GetOptions() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Options
}

func (s *Stream) readLoop(ctx context.Context) error {
	s.mu.RLock()
	remote := s.remote
	s.mu.RUnlock()

	if remote == nil {
		return fmt.Errorf("remote not connected")
	}

	remote.connMutex.Lock()
	conn := remote.RemoteConn
	remote.connMutex.Unlock()

	if conn == nil {
		return fmt.Errorf("remote connection is nil")
	}

	buffer := make([]byte, GlobalConfig.BufferSize)
	length := 0

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// Process everything we currently have in the buffer
		for length > 0 {
			if buffer[0] == '$' {
				if length < 4 {
					break // Need more data for header
				}
				pktLen := (int(buffer[2]) << 8) | int(buffer[3])
				if length < 4+pktLen {
					break // Need more data for payload
				}

				packet := make([]byte, 4+pktLen)
				copy(packet, buffer[:4+pktLen])

				if verbose.Load() && (buffer[1] == 0 || buffer[1] == 2) {
					Logf("📦 [MEDIA] Received RTP packet from camera, channel %d, len: %d", buffer[1], pktLen)
				}

				s.dispatch(int(buffer[1]), packet)

				// Shift buffer
				copy(buffer[0:], buffer[4+pktLen:length])
				length -= (4 + pktLen)
				continue // Try to process next item
			} else {
				// RTSP message
				eol := bytes.Index(buffer[:length], []byte("\r\n\r\n"))
				if eol == -1 {
					break // Need more data for headers
				}

				headerPart := string(buffer[:eol])
				contentLength := 0
				contentLengthMatch := regexp.MustCompile(`(?i)\r\nContent-Length:\s*(\d+)\r\n`).FindStringSubmatch(headerPart)
				if len(contentLengthMatch) > 1 {
					contentLength, _ = strconv.Atoi(contentLengthMatch[1])
				}

				totalMsgLen := eol + 4 + contentLength
				if length < totalMsgLen {
					break // Need more data for body
				}

				msg := string(buffer[:totalMsgLen])
				s.remote.HandleUpstreamResponse(msg)

				copy(buffer[0:], buffer[totalMsgLen:length])
				length -= totalMsgLen
				continue // Try to process next item
			}
		}

		if length == len(buffer) {
			LogCriticalf("Stream [%s] buffer full, clearing", s.Path)
			length = 0
		}

		// Read more data
		conn.SetReadDeadline(time.Now().Add(GlobalConfig.ReadTimeout))
		n, err := conn.Read(buffer[length:])
		conn.SetReadDeadline(time.Time{})

		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return err
		}
		length += n
	}
}

func (s *Stream) dispatch(channel int, packet []byte) {
	now := time.Now()
	atomic.AddUint64(&s.PacketsForwarded, 1)
	atomic.AddUint64(&s.BytesForwarded, uint64(len(packet)))

	s.mu.Lock()
	s.LastPktTime = now
	s.mu.Unlock()

	s.mu.RLock()
	defer s.mu.RUnlock()

	for client, cs := range s.clients {
		clientChannel, ok := cs.channels[channel]
		if !ok {
			// Logf("Stream [%s] no mapping for channel %d for client %s", s.Path, channel, client.remoteAddr)
			continue
		}

		// Optimization: only copy if we need to modify the channel byte
		// Since we modify it for every client, we MUST copy.
		clientPacket := make([]byte, len(packet))
		copy(clientPacket, packet)
		clientPacket[1] = byte(clientChannel)

		if !cs.Push(clientPacket) {
			atomic.AddUint64(&s.PacketsDropped, 1)
			LogCriticalf("Stream [%s] dropping packet for slow client %s", s.Path, client.remoteAddr)
		}
	}
}

// GetBitrate returns the current bitrate in bits per second.
func (s *Stream) GetBitrate() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	elapsed := time.Since(s.StartTime).Seconds()
	if elapsed < 1 {
		return 0
	}
	return uint64(float64(atomic.LoadUint64(&s.BytesForwarded)*8) / elapsed)
}

// ReportMetrics logs the current stream metrics.
func (s *Stream) ReportMetrics() {
	s.mu.RLock()
	defer s.mu.RUnlock()

	bitrate := s.GetBitrate()
	duration := time.Since(s.StartTime).Seconds()

	avgQueue := 0
	if len(s.clients) > 0 {
		totalDepth := 0
		for _, cs := range s.clients {
			totalDepth += cs.QueueDepth()
		}
		avgQueue = totalDepth / len(s.clients)
	}

	Logf("📊 Stream [%s] Metrics:", s.Path)
	Logf("  State: %s", s.state)
	Logf("  Clients: %d", len(s.clients))
	Logf("  Reconnects: %d (Total Downtime: %v)", atomic.LoadUint64(&s.ReconnectCount), s.ReconnectTotal)
	Logf("  Packets (Fwd/Drop): %d / %d", atomic.LoadUint64(&s.PacketsForwarded), atomic.LoadUint64(&s.PacketsDropped))
	Logf("  Throughput: %d bytes (Avg %d bps)", atomic.LoadUint64(&s.BytesForwarded), bitrate)
	Logf("  Avg Queue Depth: %d", avgQueue)
	Logf("  Uptime: %.1fs", duration)
	if !s.LastPktTime.IsZero() {
		Logf("  Last Packet: %s ago", time.Since(s.LastPktTime).Truncate(time.Millisecond))
	}
}

func (s *Stream) parseTracks(sdp string) []string {
	var tracks []string
	lines := strings.Split(sdp, "\n")
	inMedia := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "m=") {
			inMedia = true
		}
		if inMedia && strings.HasPrefix(line, "a=control:") {
			control := strings.TrimPrefix(line, "a=control:")
			if control != "*" { // 🔥 Игнорируем wildcard на уровне сессии
				if !strings.HasPrefix(control, "rtsp://") {
					tracks = append(tracks, control)
				} else {
					tracks = append(tracks, control)
				}
			}
		}
	}

	if len(tracks) == 0 {
		// Fallback: если явных треков нет, используем базовый URL
		tracks = append(tracks, "")
	}
	return tracks
}

// MapChannel records a mapping from upstream channel to client channel.
func (s *Stream) MapChannel(client *Client, upstreamChan, clientChan int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cs, ok := s.clients[client]; ok {
		cs.channels[upstreamChan] = clientChan
	}
}

// LookupTransport retrieves an existing transport for a substream.
func (s *Stream) LookupTransport(substreamName, protocol, comType string) *Transport {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.remote == nil {
		return nil
	}

	return s.remote.LookupTransport(s.Path, substreamName, protocol, comType)
}

// LookupSession retrieves an existing session or creates a new one for the stream.
func (s *Stream) LookupSession(sessionID string, args ...int) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	timeout := 60
	if len(args) > 0 {
		timeout = args[0]
	}

	if session, ok := s.sessions[sessionID]; ok {
		return session
	}

	session := NewSession(nil, sessionID, timeout)
	s.sessions[sessionID] = session
	return session
}
