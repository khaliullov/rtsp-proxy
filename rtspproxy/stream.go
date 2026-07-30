package rtspproxy

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// packetPool manages reusable byte buffers for RTP packets to reduce GC pressure.
var packetPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, GlobalConfig.BufferSize)
	},
}

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
	PacketsForwarded      uint64
	PacketsDropped        uint64
	BytesForwarded        uint64
	SessionBytesForwarded uint64
	ReconnectCount        uint64
	LastReconnect         time.Time
	ReconnectTotal        time.Duration
	StartTime             time.Time
	SessionStartTime      time.Time
	LastPktTime           time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	IdleTimeout time.Duration

	// Signals for on-demand connection
	readyCh    chan struct{}
	sdpReadyCh chan struct{}
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
		readyCh:     make(chan struct{}),
		sdpReadyCh:  make(chan struct{}),
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

	// If we are starting a new connection attempt, ensure old waiters are released
	if (s.state == StateDisconnected || s.state == StateReconnecting) && state == StateConnecting {
		if s.readyCh != nil {
			select {
			case <-s.readyCh:
				// Already closed
			default:
				close(s.readyCh)
			}
		}
		if s.sdpReadyCh != nil {
			select {
			case <-s.sdpReadyCh:
				// Already closed
			default:
				close(s.sdpReadyCh)
			}
		}
		s.readyCh = make(chan struct{})
		s.sdpReadyCh = make(chan struct{})
	}

	// Close ready signal when entering Playing state
	if state == StatePlaying {
		s.SessionStartTime = time.Now()
		atomic.StoreUint64(&s.SessionBytesForwarded, 0)
		select {
		case <-s.readyCh:
		default:
			close(s.readyCh)
		}
	}

	Logf("Stream [%s] state change: %s -> %s", s.Path, s.state, state)
	s.state = state

	// If destroyed, close everything one last time
	if state == StateDestroyed {
		if s.readyCh != nil {
			select {
			case <-s.readyCh:
			default:
				close(s.readyCh)
			}
		}
		if s.sdpReadyCh != nil {
			select {
			case <-s.sdpReadyCh:
			default:
				close(s.sdpReadyCh)
			}
		}
	}
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
		valid = to == StatePlaying || to == StateReconnecting || to == StateStopping || to == StateDestroyed || to == StateConnecting
	case StatePlaying:
		valid = to == StateReconnecting || to == StateStopping || to == StateDestroyed || to == StatePlaying
	case StateReconnecting:
		valid = to == StatePlaying || to == StateDisconnected || to == StateStopping || to == StateDestroyed || to == StateConnecting || to == StateReconnecting
	case StateStopping:
		valid = to == StateDisconnected || to == StateDestroyed || to == StateStopping
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

	s.unsafeSetState(StateConnecting)
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

// Stop closes the upstream connection and clears sessions.
func (s *Stream) Stop() {
	if err := s.transition(StateStopping); err != nil {
		return
	}
	s.mu.Lock()
	remote := s.remote
	sessions := s.sessions
	s.sessions = make(map[string]*Session)
	s.mu.Unlock()

	if remote != nil {
		for id := range sessions {
			_ = remote.SendTeardown(s.Path, id)
		}
		remote.Disconnect()
	}

	for _, sess := range sessions {
		sess.Stop()
	}

	s.transition(StateDisconnected)
}

// Destroy cleans up all resources.
func (s *Stream) Destroy() {
	s.transition(StateDestroyed)
	s.cancel()

	s.mu.Lock()
	remote := s.remote
	s.remote = nil
	clients := s.clients
	s.clients = make(map[*Client]*ClientSession)
	sessions := s.sessions
	s.sessions = make(map[string]*Session)
	s.stopIdleTimer()
	s.mu.Unlock()

	if remote != nil {
		for id := range sessions {
			_ = remote.SendTeardown(s.Path, id)
		}
		remote.Disconnect()
	}

	for _, cs := range clients {
		cs.Stop()
	}

	for _, sess := range sessions {
		sess.Stop()
	}

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
			GlobalMetrics.Reconnects.Add(1)
			s.mu.Lock()
			if s.LastReconnect.IsZero() {
				s.LastReconnect = time.Now()
			}
			s.mu.Unlock()
		}

		if err := s.transition(StateConnecting); err != nil {
			return
		}

		s.mu.Lock()
		if s.remote != nil {
			s.remote.Disconnect()
		}
		s.remote = NewRemote(s)
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
				s.mu.Lock()
				if !s.LastReconnect.IsZero() {
					s.ReconnectTotal += time.Since(s.LastReconnect)
					s.LastReconnect = time.Time{}
				}
				s.mu.Unlock()

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

	// Options/Server are written by remote.handleOptions into this same Stream.

	// 2. DESCRIBE
	sdp, err := remote.GetSDP(s.Path)
	if err != nil {
		return fmt.Errorf("DESCRIBE failed: %w", err)
	}
	s.mu.Lock()
	s.SDP = sdp
	select {
	case <-s.sdpReadyCh:
	default:
		close(s.sdpReadyCh)
	}
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

// ReadyCh returns a channel that is closed when the stream enters Playing state.
func (s *Stream) ReadyCh() chan struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readyCh
}

// SDPReadyCh returns a channel that is closed when the SDP is available.
func (s *Stream) SDPReadyCh() chan struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sdpReadyCh
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

				headerPart := buffer[:eol]
				contentLength := sharedParseContentLength(headerPart)

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

// targetSnapshot describes a single client delivery point.
type targetSnapshot struct {
	cs            *ClientSession
	clientChannel int
	remoteAddr    string
}

// targetPool avoids allocation for fan-out snapshots.
var targetPool = sync.Pool{
	New: func() interface{} {
		return make([]targetSnapshot, 0, 10)
	},
}

func (s *Stream) dispatch(channel int, packet []byte) {
	now := time.Now()
	atomic.AddUint64(&s.PacketsForwarded, 1)
	atomic.AddUint64(&s.BytesForwarded, uint64(len(packet)))
	atomic.AddUint64(&s.SessionBytesForwarded, uint64(len(packet)))
	GlobalMetrics.PacketsForwarded.Add(1)
	GlobalMetrics.BytesForwarded.Add(uint64(len(packet)))

	s.mu.Lock()
	s.LastPktTime = now

	targets := targetPool.Get().([]targetSnapshot)[:0]
	for client, cs := range s.clients {
		clientChannel, ok := cs.channels[channel]
		if !ok {
			continue
		}
		targets = append(targets, targetSnapshot{cs: cs, clientChannel: clientChannel, remoteAddr: client.remoteAddr})
	}
	s.mu.Unlock()

	for _, t := range targets {
		buf := packetPool.Get().([]byte)
		clientPacket := buf[:len(packet)]
		copy(clientPacket, packet)
		clientPacket[1] = byte(t.clientChannel)

		if !t.cs.Push(clientPacket) {
			atomic.AddUint64(&s.PacketsDropped, 1)
			GlobalMetrics.PacketsDropped.Add(1)
			LogCriticalf("Stream [%s] dropping packet for slow client %s", s.Path, t.remoteAddr)
			packetPool.Put(buf)
		}
	}

	targetPool.Put(targets)
}

// GetBitrate returns the current bitrate in bits per second for the active session.
func (s *Stream) GetBitrate() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	start := s.SessionStartTime
	if start.IsZero() {
		start = s.StartTime
	}

	elapsed := time.Since(start).Seconds()
	if elapsed < 1 {
		return 0
	}
	return uint64(float64(atomic.LoadUint64(&s.SessionBytesForwarded)*8) / elapsed)
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
	baseURL := ""

	// Collect session-level control (for resolving relative tracks)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "m=") {
			break
		}
		if strings.HasPrefix(line, "a=control:") {
			ctrl := strings.TrimPrefix(line, "a=control:")
			if ctrl != "*" {
				baseURL = ctrl
			}
		}
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "m=") {
			inMedia = true
			continue
		}
		if !inMedia {
			continue
		}
		// New media section resets relative context but we keep collecting all tracks
		if strings.HasPrefix(line, "a=control:") {
			control := strings.TrimPrefix(line, "a=control:")
			if control == "*" {
				continue // session-level wildcard, skip
			}
			if strings.HasPrefix(control, "rtsp://") {
				// Absolute control URL — keep as-is (SetupUpstream handles it)
				tracks = append(tracks, control)
			} else if baseURL != "" && strings.HasPrefix(baseURL, "rtsp://") {
				// Resolve relative track against session control base
				base := strings.TrimRight(baseURL, "/")
				tracks = append(tracks, base+"/"+strings.TrimLeft(control, "/"))
			} else {
				tracks = append(tracks, control)
			}
		}
	}

	if len(tracks) == 0 {
		// Fallback: no explicit tracks — use base path
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

	session := NewSession(s, sessionID, timeout)
	s.sessions[sessionID] = session
	return session
}
