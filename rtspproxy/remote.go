package rtspproxy

import (
	"container/list"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Remote represents a connection to a remote RTSP server.
// It is always owned by a single *Stream (no internal Stream map).
type Remote struct {
	Host        string
	RemoteConn  *net.TCPConn
	localPort   string
	remotePort  string
	localAddr   string
	remoteAddr  string
	currentCSeq int
	Server      *Server
	stream      *Stream // parent Stream from StreamManager — sole source of truth
	connMutex   sync.Mutex
	addr        *net.TCPAddr
	requests    *list.List
	digest      *Digest
}

// NewRemote creates a new Remote bound to the given Stream.
func NewRemote(stream *Stream) *Remote {
	host := stream.Host
	addr, err := net.ResolveTCPAddr("tcp", host)
	if err != nil {
		LogCriticalf("Failed to resolve TCP address for host %q: %s", host, err.Error())
		return nil
	}

	remote := &Remote{
		Host:     host,
		Server:   stream.server,
		stream:   stream,
		addr:     addr,
		requests: list.New(),
		digest:   NewDigest(),
	}
	if stream.Username != "" {
		remote.digest.Username = stream.Username
		remote.digest.Password = stream.Password
	}
	return remote
}

// Dial establishes a connection to the remote RTSP server.
func (remote *Remote) Dial() error {
	remote.connMutex.Lock()
	defer remote.connMutex.Unlock()
	return remote.dialLocked()
}

// Disconnect closes the socket and clears internal state.
func (remote *Remote) Disconnect() {
	remote.connMutex.Lock()
	defer remote.connMutex.Unlock()

	if remote.RemoteConn != nil {
		remote.RemoteConn.Close()
		remote.RemoteConn = nil

		remote.digest.Nonce = ""
		remote.digest.Nc = 0
		if remote.requests != nil {
			remote.requests.Init()
		}
		remote.currentCSeq = 0

		LogCriticalf("Remote connection closed [%s]. State cleared.", remote.Host)
	}
}

// Destroy is for full cleanup (e.g., server shutdown).
func (remote *Remote) Destroy() error {
	remote.Disconnect()
	return nil
}

// HandleUpstreamResponse processes a raw RTSP message received from the camera.
func (remote *Remote) HandleUpstreamResponse(recv string) {
	response, err := NewResponseFromBuffer(recv)
	if err != nil {
		LogCriticalf("remote rtsp read request error: %v", err)
		return
	}

	remote.connMutex.Lock()
	requestEl := remote.requests.Front()
	if requestEl == nil {
		remote.connMutex.Unlock()
		LogCriticalf("⚠️ [QUEUE] Received response but queue is empty! Dropping.")
		return
	}
	request := requestEl.Value.(*Request)
	// We don't remove yet, in case we need to retry (401)
	remote.connMutex.Unlock()

	status := "ok"

	if response.Code == 401 && request.Attempts == 0 {
		wwwAuthenticate := headerGet(response.Headers, "WWW-Authenticate")
		if wwwAuthenticate != "" {
			if remote.digest.Username != "" && remote.digest.Password != "" && remote.handleAuthenticationFailure(wwwAuthenticate) {
				request.Attempts++
				Logf("🔑 [AUTH] Retrying with Digest auth (CSeq will be updated)...")
				_ = remote.SendRequest(request)
				return
			}
			LogCriticalf("❌ [AUTH] Auth failed or missing credentials.")
			GlobalMetrics.AuthFailures.Add(1)
			status = "unauthorized"
		}
	} else {
		if response.Code >= 400 {
			status = fmt.Sprintf("error %d: %s", response.Code, response.Status)
			LogCriticalf("⚠️ [RTSP] Camera returned error for %s: %s", request.Method, status)
		} else {
			switch request.Method {
			case "OPTIONS":
				remote.handleOptions(request, response)
			case "DESCRIBE":
				remote.handleDescribe(request, response)
			case "SETUP":
				remote.handleSetup(request, response)
			case "PLAY":
				remote.handlePlay(request, response)
			case "TEARDOWN":
				remote.handleTeardown(request, response)
			}
		}
	}

	// Finalize request: remove from queue and notify subscribers
	remote.connMutex.Lock()
	found := false
	for e := remote.requests.Front(); e != nil; e = e.Next() {
		if e == requestEl {
			remote.requests.Remove(e)
			found = true
			break
		}
	}
	remote.connMutex.Unlock()

	if found {
		for e := request.Subscriptions.Front(); e != nil; e = e.Next() {
			ch := e.Value.(chan string)
			func() {
				defer func() { recover() }()
				select {
				case ch <- status:
				default:
				}
			}()
		}
	}
}

func (remote *Remote) handleTeardown(request *Request, response *Response) {
	stream := remote.stream
	if stream == nil {
		return
	}
	sessionID := headerGet(request.Headers, "Session")
	session := stream.LookupSession(sessionID)
	session.Stop()
	stream.mu.Lock()
	delete(stream.sessions, sessionID)
	stream.mu.Unlock()
}

func (remote *Remote) handleOptions(request *Request, response *Response) {
	stream := remote.stream
	if stream == nil {
		return
	}
	stream.mu.Lock()
	stream.Options = headerGet(response.Headers, "Public")
	stream.Server = headerGet(response.Headers, "Server")
	stream.mu.Unlock()
}

func (remote *Remote) handleDescribe(request *Request, response *Response) {
	stream := remote.stream
	if stream == nil {
		return
	}
	stream.mu.Lock()
	stream.SDP = response.Body
	stream.mu.Unlock()
}

func (remote *Remote) parseTransport(transportStr string) (string, string, map[string]string) {
	if transportStr == "" {
		return "", "", make(map[string]string)
	}
	transportParts := strings.Split(transportStr, ";")
	protocol := transportParts[0]
	comType := "unicast"
	if len(transportParts) > 1 {
		comType = transportParts[1]
	}
	params := make(map[string]string)
	if len(transportParts) > 2 {
		for _, element := range transportParts[2:] {
			kv := strings.SplitN(element, "=", 2)
			if len(kv) == 2 {
				params[kv[0]] = kv[1]
			}
		}
	}
	return protocol, comType, params
}

func (remote *Remote) handleSetup(request *Request, response *Response) {
	stream := remote.stream
	if stream == nil {
		return
	}
	_, substreamName := filepath.Split(request.URL.Path)
	protocol, comType, params := remote.parseTransport(headerGet(response.Headers, "Transport"))
	sessionParams := strings.Split(headerGet(response.Headers, "Session"), ";")
	session := stream.LookupSession(sessionParams[0])
	transport := session.LookupTransport(substreamName, protocol, comType)

	transport.mu.Lock()
	transport.Ssrc = params["ssrc"]

	if interleaved, ok := params["interleaved"]; ok {
		channels := strings.Split(interleaved, "-")
		ch1, _ := strconv.Atoi(channels[0])
		sub1 := NewSubstream(transport, substreamName)
		sub1.Channel = ch1
		transport.Substreams[0] = sub1
		if len(channels) > 1 {
			ch2, _ := strconv.Atoi(channels[1])
			sub2 := NewSubstream(transport, substreamName)
			sub2.Channel = ch2
			transport.Substreams[1] = sub2
		}
	}
	transport.mu.Unlock()

	if len(sessionParams) > 1 {
		for _, element := range sessionParams[1:] {
			kv := strings.SplitN(element, "=", 2)
			if len(kv) == 2 && kv[0] == "timeout" {
				session.Timeout, _ = strconv.Atoi(kv[1])
			}
		}
	}
}

func (remote *Remote) handlePlay(request *Request, response *Response) {
	stream := remote.stream
	if stream == nil {
		return
	}
	rtpInfo := headerGet(response.Headers, "RTP-Info")
	session := stream.LookupSession(headerGet(request.Headers, "Session"))

	if rtpInfo == "" {
		session.StartUpstream()
		return
	}

	for _, rtp := range strings.Split(rtpInfo, ",") {
		params := make(map[string]string)
		for _, param := range strings.Split(rtp, ";") {
			kv := strings.SplitN(param, "=", 2)
			if len(kv) == 2 {
				params[kv[0]] = kv[1]
			}
		}
		URL, _ := url.Parse(params["url"])
		_, substreamName := filepath.Split(URL.Path)

		session.mu.Lock()
		for e := session.Transports.Front(); e != nil; e = e.Next() {
			transport := e.Value.(*Transport)
			if transport.SubstreamName == substreamName {
				transport.mu.Lock()
				if sub, ok := transport.Substreams[0]; ok {
					sub.Seq, _ = strconv.Atoi(params["seq"])
					sub.RTPTime, _ = strconv.Atoi(params["rtptime"])
				}
				transport.mu.Unlock()
			}
		}
		session.mu.Unlock()
	}
	session.StartUpstream()
}

// GetOptions retrieves the OPTIONS response for a given stream.
func (remote *Remote) GetOptions(streamName string) (string, error) {
	stream := remote.stream
	if stream == nil {
		return "", errors.New("no stream bound")
	}
	if stream.GetOptions() == "" {
		URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: streamName}
		request, _ := NewRequest("OPTIONS", URL)
		err := remote.SendRequestSync(request)
		if err != nil {
			return "", err
		}
	}
	return stream.GetOptions(), nil
}

// GetSDP retrieves the SDP description for a given stream.
func (remote *Remote) GetSDP(streamName string) (string, error) {
	stream := remote.stream
	if stream == nil {
		return "", errors.New("no stream bound")
	}
	if stream.GetSDP() == "" {
		URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: streamName}
		request, _ := NewRequest("DESCRIBE", URL)
		err := remote.SendRequestSync(request)
		if err != nil {
			return "", err
		}
	}
	return stream.GetSDP(), nil
}

// SetupUpstream performs a SETUP request for the upstream connection.
func (remote *Remote) SetupUpstream(stream *Stream, track, transportStr string) (string, string, error) {
	var reqURL *url.URL

	if track == "" || track == "*" {
		reqURL = &url.URL{Scheme: "rtsp", Host: remote.Host, Path: stream.Path}
	} else if strings.HasPrefix(track, "rtsp://") {
		reqURL, _ = url.Parse(track)
	} else {
		basePath := strings.TrimRight(stream.Path, "/")
		trackPath := strings.TrimLeft(track, "/")
		fullPath := basePath + "/" + trackPath
		reqURL = &url.URL{Scheme: "rtsp", Host: remote.Host, Path: fullPath}
	}

	request, _ := NewRequest("SETUP", reqURL)
	request.Headers["Transport"] = transportStr
	err := remote.SendRequestSync(request)
	if err != nil {
		return "", "", err
	}

	stream.mu.RLock()
	defer stream.mu.RUnlock()
	for _, sess := range stream.sessions {
		sess.mu.RLock()
		for e := sess.Transports.Front(); e != nil; e = e.Next() {
			t := e.Value.(*Transport)
			base := filepath.Base(track)
			if t.SubstreamName == track ||
				(track == "" && t.SubstreamName == filepath.Base(stream.Path)) ||
				(track != "" && (t.SubstreamName == base || t.SubstreamName == track)) {
				t.mu.RLock()
				ssrc := t.Ssrc
				t.mu.RUnlock()
				sess.mu.RUnlock()
				return ssrc, sess.Session, nil
			}
		}
		sess.mu.RUnlock()
	}

	return "", "", errors.New("failed to find transport after SETUP")
}

// PlayUpstream performs a PLAY request for the upstream connection.
func (remote *Remote) PlayUpstream(path, sessionID string) (string, error) {
	URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: path}
	request, _ := NewRequest("PLAY", URL)
	request.Headers["Session"] = sessionID
	request.Headers["Range"] = "npt=0.000-"

	err := remote.SendRequestSync(request)
	if err != nil {
		return "", err
	}

	stream := remote.stream
	if stream != nil {
		stream.mu.RLock()
		sess, ok := stream.sessions[sessionID]
		stream.mu.RUnlock()
		if ok {
			sess.StartUpstream()
		}
	}

	return "ok", nil
}

// LookupTransport retrieves an existing transport for a substream from the parent Stream.
func (remote *Remote) LookupTransport(streamName, substreamName, protocol, comType string) *Transport {
	stream := remote.stream
	if stream == nil {
		return nil
	}
	stream.mu.RLock()
	defer stream.mu.RUnlock()
	for _, session := range stream.sessions {
		session.mu.RLock()
		for e := session.Transports.Front(); e != nil; e = e.Next() {
			transport := e.Value.(*Transport)
			if transport.SubstreamName == substreamName && transport.Protocol == protocol && transport.ComType == comType {
				session.mu.RUnlock()
				return transport
			}
		}
		session.mu.RUnlock()
	}
	return nil
}

// SendRequestSync sends an RTSP request and waits for a synchronous response.
func (remote *Remote) SendRequestSync(request *Request) error {
	for attempt := 0; attempt < 2; attempt++ {
		ipc := NewIPC(10)

		if request.Subscriptions == nil {
			request.Subscriptions = list.New()
		} else {
			request.Subscriptions.Init()
		}
		request.Subscriptions.PushBack(ipc.Channel)

		remote.connMutex.Lock()
		remote.requests.PushBack(request)
		remote.connMutex.Unlock()

		err := remote.SendRequest(request)
		if err != nil {
			remote.connMutex.Lock()
			for e := remote.requests.Front(); e != nil; e = e.Next() {
				if e.Value.(*Request) == request {
					remote.requests.Remove(e)
					break
				}
			}
			remote.connMutex.Unlock()
			return err
		}

		result := ipc.GetResponse(remote.stream.ctx)

		select {
		case <-remote.stream.ctx.Done():
			return errors.New("stream destroyed, request aborted")
		case <-remote.Server.ctx.Done():
			return errors.New("server is shutting down, request aborted")
		default:
		}

		if result == "timeout" {
			remote.connMutex.Lock()
			for e := remote.requests.Front(); e != nil; e = e.Next() {
				if e.Value.(*Request) == request {
					remote.requests.Remove(e)
					break
				}
			}

			if attempt == 0 {
				Logf("⚠️ [AUTH] Request timed out (camera silently dropped stale nonce). Clearing nonce and retrying...")
				remote.digest.Nonce = ""
				remote.digest.Nc = 0
				remote.connMutex.Unlock()
				request.Attempts = 0
				continue
			}
			remote.connMutex.Unlock()
		}

		if result != "ok" {
			return errors.New(result)
		}

		return nil
	}

	return errors.New("timeout after retry")
}

// SendRequest sends an RTSP request to the remote server.
// Enforces strict atomicity for the entire request string.
func (remote *Remote) SendRequest(request *Request) error {
	remote.connMutex.Lock()
	defer remote.connMutex.Unlock()

	if remote.RemoteConn == nil {
		err := remote.dialLocked()
		if err != nil {
			return err
		}
	}
	conn := remote.RemoteConn

	remote.currentCSeq++
	request.Headers["CSeq"] = strconv.Itoa(remote.currentCSeq)
	remote.createAuthenticatorStr(request)

	rawRequest := []byte(request.String())
	Logf("📤 RAW REQUEST TO CAMERA [%s]:\n%s", remote.Host, string(rawRequest))

	conn.SetWriteDeadline(time.Now().Add(GlobalConfig.WriteTimeout))
	_, err := conn.Write(rawRequest)
	conn.SetWriteDeadline(time.Time{})
	if err != nil {
		remote.disconnectLocked()
		return fmt.Errorf("failed to write to remote: %w", err)
	}
	return nil
}

func (remote *Remote) dialLocked() error {
	select {
	case <-remote.Server.ctx.Done():
		return fmt.Errorf("server is shutting down")
	default:
		dialer := net.Dialer{Timeout: GlobalConfig.DialTimeout}
		socket, err := dialer.DialContext(remote.Server.ctx, "tcp", remote.Host)
		if err != nil {
			GlobalMetrics.ConnectErrors.Add(1)
			return fmt.Errorf("failed to connect to %q: %w", remote.Host, err)
		}

		localAddr := strings.Split(socket.LocalAddr().String(), ":")
		remoteAddr := strings.Split(socket.RemoteAddr().String(), ":")

		if remote.RemoteConn != nil {
			remote.RemoteConn.Close()
		}
		remote.RemoteConn, _ = socket.(*net.TCPConn)
		remote.localAddr = localAddr[0]
		remote.localPort = localAddr[1]
		remote.remoteAddr = remoteAddr[0]
		remote.remotePort = remoteAddr[1]
		remote.currentCSeq = 0

		return nil
	}
}

// SendTeardown sends a TEARDOWN request for a specific session.
func (remote *Remote) SendTeardown(path, sessionID string) error {
	URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: path}
	request, _ := NewRequest("TEARDOWN", URL)
	request.Headers["Session"] = sessionID
	return remote.SendRequest(request)
}

// binaryHeaderPool avoids allocation for interleaved data headers.
var binaryHeaderPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 4)
	},
}

// SendBinary forwards interleaved RTP/RTCP data from client to remote.
// Enforces strict atomicity to prevent stream corruption.
func (remote *Remote) SendBinary(channel int, data []byte) error {
	remote.connMutex.Lock()
	defer remote.connMutex.Unlock()

	if remote.RemoteConn == nil {
		LogCriticalf("⚠️ SendBinary failed: remote connection is closed")
		return errors.New("remote connection is closed")
	}
	conn := remote.RemoteConn

	header := binaryHeaderPool.Get().([]byte)
	defer binaryHeaderPool.Put(header)

	header[0] = '$'
	header[1] = byte(channel)
	header[2] = byte((len(data) & 0xFF00) >> 8)
	header[3] = byte(len(data) & 0xFF)

	conn.SetWriteDeadline(time.Now().Add(GlobalConfig.WriteTimeout))
	_, err := conn.Write(header)
	if err != nil {
		conn.SetWriteDeadline(time.Time{})
		LogCriticalf("⚠️ SendBinary header failed: %v", err)
		remote.disconnectLocked()
		return fmt.Errorf("failed to write binary header: %w", err)
	}

	_, err = conn.Write(data)
	conn.SetWriteDeadline(time.Time{})
	if err != nil {
		LogCriticalf("⚠️ SendBinary data failed: %v", err)
		remote.disconnectLocked()
		return fmt.Errorf("failed to write binary data: %w", err)
	}
	return nil
}

// disconnectLocked is used internally when connMutex is already held.
func (remote *Remote) disconnectLocked() {
	if remote.RemoteConn != nil {
		remote.RemoteConn.Close()
		remote.RemoteConn = nil
		remote.digest.Nonce = ""
		remote.digest.Nc = 0
		if remote.requests != nil {
			remote.requests.Init()
		}
		remote.currentCSeq = 0
		LogCriticalf("Remote connection closed [%s] due to error.", remote.Host)
	}
}

func (remote *Remote) handleAuthenticationFailure(paramsStr string) bool {
	if paramsStr == "" {
		return false
	}
	remote.connMutex.Lock()
	defer remote.connMutex.Unlock()
	if remote.digest.Username == "" || remote.digest.Password == "" {
		return false
	}

	realm, nonce, qop, opaque, algorithm, isDigest := ParseWWWAuthenticate(paramsStr)
	if isDigest {
		remote.digest.Realm = realm
		remote.digest.Nonce = nonce
		remote.digest.Qop = qop
		remote.digest.Opaque = opaque
		remote.digest.Algorithm = algorithm
		remote.digest.Nc = 0 // reset nonce count on new challenge
		Logf("✅ [AUTH] Updated Digest: Realm=%q, Nonce=%q, Qop=%q", remote.digest.Realm, remote.digest.Nonce, remote.digest.Qop)
		return true
	}
	if realm != "" {
		// Basic
		remote.digest.Realm = realm
		remote.digest.Nonce = ""
		return true
	}
	return false
}

func (remote *Remote) createAuthenticatorStr(request *Request) {
	if remote.digest.Realm == "" || remote.digest.Username == "" || remote.digest.Password == "" {
		return
	}
	URL := request.GetURL().String()
	if remote.digest.Nonce != "" {
		response, ncStr, cnonce := remote.digest.ComputeResponse(request.Method, URL)
		var auth strings.Builder
		auth.WriteString(fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
			remote.digest.Username, remote.digest.Realm, remote.digest.Nonce, URL, response))
		if remote.digest.Qop != "" {
			auth.WriteString(fmt.Sprintf(`, qop=%s, nc=%s, cnonce="%s"`, remote.digest.Qop, ncStr, cnonce))
		}
		if remote.digest.Opaque != "" {
			auth.WriteString(fmt.Sprintf(`, opaque="%s"`, remote.digest.Opaque))
		}
		if remote.digest.Algorithm != "" && !strings.EqualFold(remote.digest.Algorithm, "MD5") {
			auth.WriteString(fmt.Sprintf(`, algorithm=%s`, remote.digest.Algorithm))
		}
		request.Headers["Authorization"] = auth.String()
	} else {
		usernamePassword := fmt.Sprintf("%s:%s", remote.digest.Username, remote.digest.Password)
		encoded := base64.StdEncoding.EncodeToString([]byte(usernamePassword))
		request.Headers["Authorization"] = fmt.Sprintf("Basic %s", encoded)
	}
}
