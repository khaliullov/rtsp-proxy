package rtspproxy

import (
	"container/list"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Remote represents a connection to a remote RTSP server.
type Remote struct {
	Host        string
	RemoteConn  *net.TCPConn
	localPort   string
	remotePort  string
	localAddr   string
	remoteAddr  string
	currentCSeq int
	Server      *Server
	connMutex   sync.Mutex
	addr        *net.TCPAddr
	streams     map[string]*Stream // Internal tracking for RTSP state
	requests    *list.List
	digest      *Digest
}

// LookupStream retrieves an existing stream or creates a new one if it doesn't exist.
func (remote *Remote) LookupStream(streamName string) *Stream {
	if stream, ok := remote.streams[streamName]; ok {
		return stream
	}
	stream := &Stream{
		Path:     streamName,
		sessions: make(map[string]*Session),
	}
	remote.streams[streamName] = stream
	return stream
}

// NewRemote creates a new Remote instance.
func NewRemote(server *Server, host, username, password string) *Remote {
	Logf("DEBUG: NewRemote called with host: %q, username: %q", host, username)
	addr, err := net.ResolveTCPAddr("tcp", host)
	if err != nil {
		LogCriticalf("Failed to resolve TCP address for host %q: %s", host, err.Error())
		return nil
	}

	remote := &Remote{
		Host:     host,
		Server:   server,
		addr:     addr,
		streams:  make(map[string]*Stream),
		requests: list.New(),
		digest:   NewDigest(),
	}
	if username != "" {
		remote.digest.Username = username
		remote.digest.Password = password
	}
	Logf("DEBUG: NewRemote created for host: %q", remote.Host)
	// Connect on-demand: Dial() is no longer called here.
	// It will be triggered by the first SendRequest.
	return remote
}

// Dial establishes a connection to the remote RTSP server.
func (remote *Remote) Dial() error {
	Logf("DEBUG: Dialing remote host: %q", remote.Host)
	select {
	case <-remote.Server.ctx.Done():
		return fmt.Errorf("server is shutting down")
	default:
		dialer := net.Dialer{Timeout: GlobalConfig.DialTimeout}
		socket, err := dialer.DialContext(remote.Server.ctx, "tcp", remote.Host)
		if err != nil {
			return fmt.Errorf("failed to connect to %q: %w", remote.Host, err)
		}

		localAddr := strings.Split(socket.LocalAddr().String(), ":")
		remoteAddr := strings.Split(socket.RemoteAddr().String(), ":")

		remote.connMutex.Lock()
		if remote.RemoteConn != nil {
			remote.RemoteConn.Close()
		}
		remote.RemoteConn, _ = socket.(*net.TCPConn)
		remote.localAddr = localAddr[0]
		remote.localPort = localAddr[1]
		remote.remoteAddr = remoteAddr[0]
		remote.remotePort = remoteAddr[1]
		remote.currentCSeq = 0
		remote.connMutex.Unlock()

		return nil
	}
}

// Disconnect closes the socket but keeps the Remote object alive for auto-reconnect.
func (remote *Remote) Disconnect() {
	remote.connMutex.Lock()
	defer remote.connMutex.Unlock()

	// Выполняем очистку ТОЛЬКО если соединение действительно активно
	if remote.RemoteConn != nil {
		// 🔥 1. ЯВНО ОСТАНАВЛИВАЕМ ВСЕ ФОНОВЫЕ ГОРУТИНЫ (GET_PARAMETER)
		// Это предотвращает "зомби" запросы после разрыва соединения
		for _, stream := range remote.streams {
			for _, session := range stream.sessions {
				session.Stop()
			}
			// Очищаем мапу сессий, чтобы не было утечек памяти
			stream.sessions = make(map[string]*Session)
		}

		// 🔥 2. Закрываем сокет
		remote.RemoteConn.Close()
		remote.RemoteConn = nil

		// 🔥 3. Очищаем остальное состояние
		remote.digest.Nonce = ""
		remote.streams = make(map[string]*Stream)
		if remote.requests != nil {
			remote.requests.Init()
		}
		remote.currentCSeq = 0

		LogCriticalf("Remote disconnected [%s]. All sessions stopped. State cleared. Will reconnect on demand.", remote.Host)
	}
}

// Destroy is for full cleanup (e.g., server shutdown)
func (remote *Remote) Destroy() error {
	if remote.RemoteConn != nil {
		remote.RemoteConn.Close()
	}
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
	remote.connMutex.Unlock()

	if requestEl == nil {
		LogCriticalf("⚠️ [QUEUE] Received response but queue is empty! Dropping.")
		return
	}

	request := requestEl.Value.(*Request)
	status := "ok"

	if response.Code == 401 && request.Attempts == 0 {
		if wwwAuthenticate, ok := response.Headers["WWW-Authenticate"]; ok {
			if remote.digest.Username != "" && remote.digest.Password != "" && remote.handleAuthenticationFailure(wwwAuthenticate) {
				request.Attempts++
				Logf("🔑 [AUTH] Retrying with Digest auth (CSeq will be updated)...")
				remote.SendRequest(request)
				return // Still in queue
			} else {
				LogCriticalf("❌ [AUTH] Auth failed or missing credentials.")
				status = "unauthorized"
			}
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

	remote.connMutex.Lock()
	remote.requests.Remove(requestEl)
	remote.connMutex.Unlock()

	for e := request.Subscriptions.Front(); e != nil; e = e.Next() {
		ch := e.Value.(chan string)
		func() {
			defer func() { recover() }()
			ch <- status
		}()
		request.Subscriptions.Remove(e)
	}
}

func (remote *Remote) handleTeardown(request *Request, response *Response) {
	streamName := request.URL.Path
	remote.connMutex.Lock()
	stream := remote.streams[streamName]
	remote.connMutex.Unlock()
	if stream == nil {
		return
	}
	session := stream.LookupSession(request.Headers["Session"])
	session.Stop()
	delete(stream.sessions, request.Headers["Session"])
}

func (remote *Remote) handleOptions(request *Request, response *Response) {
	streamName := request.URL.Path
	stream := remote.lookupStreamInternal(streamName)
	stream.Options = response.Headers["Public"]
	stream.Server = response.Headers["Server"]
}

func (remote *Remote) handleDescribe(request *Request, response *Response) {
	streamName := request.URL.Path
	stream := remote.lookupStreamInternal(streamName)
	stream.SDP = response.Body
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
			kv := strings.Split(element, "=")
			if len(kv) == 2 {
				params[kv[0]] = kv[1]
			}
		}
	}
	return protocol, comType, params
}

func (remote *Remote) handleSetup(request *Request, response *Response) {
	streamName, substreamName := filepath.Split(request.URL.Path)
	streamName = filepath.Dir(streamName)
	protocol, comType, params := remote.parseTransport(response.Headers["Transport"])
	stream := remote.lookupStreamInternal(streamName)
	sessionParams := strings.Split(response.Headers["Session"], ";")
	session := stream.LookupSession(sessionParams[0])
	transport := session.LookupTransport(substreamName, protocol, comType)
	transport.Ssrc = params["ssrc"]

	if interleaved, ok := params["interleaved"]; ok {
		channels := strings.Split(interleaved, "-")
		ch1, _ := strconv.Atoi(channels[0])
		transport.Substreams[0] = NewSubstream(transport, substreamName)
		transport.Substreams[0].Channel = ch1
		if len(channels) > 1 {
			ch2, _ := strconv.Atoi(channels[1])
			transport.Substreams[1] = NewSubstream(transport, substreamName)
			transport.Substreams[1].Channel = ch2
		}
	}

	if len(sessionParams) > 1 {
		params := make(map[string]string)
		for _, element := range sessionParams[1:] {
			kv := strings.Split(element, "=")
			if len(kv) == 2 {
				params[kv[0]] = kv[1]
			}
		}
		if timeout, ok := params["timeout"]; ok {
			session.Timeout, _ = strconv.Atoi(timeout)
		}
	}
}

func (remote *Remote) handlePlay(request *Request, response *Response) {
	streamName := request.URL.Path
	stream := remote.lookupStreamInternal(streamName)
	rtpInfo := response.Headers["RTP-Info"]
	session := stream.LookupSession(request.Headers["Session"])
	transports := session.Transports

	if rtpInfo == "" {
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
		for e := transports.Front(); e != nil; e = e.Next() {
			transport := e.Value.(*Transport)
			if transport.SubstreamName == substreamName {
				transport.Substreams[0].Seq, _ = strconv.Atoi(params["seq"])
				transport.Substreams[0].RTPTime, _ = strconv.Atoi(params["rtptime"])
			}
		}
	}
	session.StartUpstream()
}

// GetOptions retrieves the OPTIONS response for a given stream.
func (remote *Remote) GetOptions(streamName string) (string, error) {
	remote.connMutex.Lock()
	stream := remote.streams[streamName]
	remote.connMutex.Unlock()

	if stream == nil || stream.Options == "" {
		// 🔥 ИСПРАВЛЕНИЕ: Используем реальный streamName, а не "/"
		URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: streamName}
		request, _ := NewRequest("OPTIONS", URL)
		err := remote.SendRequestSync(request)
		if err != nil {
			return "", err
		}
	}

	remote.connMutex.Lock()
	stream = remote.streams[streamName]
	remote.connMutex.Unlock()

	if stream != nil {
		return stream.Options, nil
	}
	return "", nil
}

// GetSDP retrieves the SDP description for a given stream.
func (remote *Remote) GetSDP(streamName string) (string, error) {
	remote.connMutex.Lock()
	stream := remote.streams[streamName]
	remote.connMutex.Unlock()

	if stream == nil || stream.SDP == "" {
		// Здесь уже было правильно: Path: streamName
		URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: streamName}
		request, _ := NewRequest("DESCRIBE", URL)
		err := remote.SendRequestSync(request)
		if err != nil {
			return "", err
		}
	}

	remote.connMutex.Lock()
	stream = remote.streams[streamName]
	remote.connMutex.Unlock()

	if stream != nil {
		return stream.SDP, nil
	}
	return "", nil
}

// SetupUpstream performs a SETUP request for the upstream connection.
func (remote *Remote) SetupUpstream(stream *Stream, track, transportStr string) (string, string, error) {
	var reqURL *url.URL

	if track == "" || track == "*" {
		reqURL = &url.URL{Scheme: "rtsp", Host: remote.Host, Path: stream.Path}
	} else if strings.HasPrefix(track, "rtsp://") {
		reqURL, _ = url.Parse(track)
	} else {
		// 🔥 Безопасное объединение путей для URL (избегаем filepath.Join и кодирования %2A)
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

	remote.connMutex.Lock()
	rs := remote.streams[stream.Path]
	remote.connMutex.Unlock()

	if rs == nil {
		return "", "", errors.New("stream not found after SETUP")
	}

	for _, sess := range rs.sessions {
		for e := sess.Transports.Front(); e != nil; e = e.Next() {
			t := e.Value.(*Transport)
			// Ищем транспорт по имени трека или базовому пути
			if t.SubstreamName == track || (track == "" && t.SubstreamName == filepath.Base(stream.Path)) || (track != "" && t.SubstreamName == filepath.Base(track)) {
				return t.Ssrc, sess.Session, nil
			}
		}
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

	remote.connMutex.Lock()
	rs := remote.streams[path]
	remote.connMutex.Unlock()

	if rs != nil {
		if sess, ok := rs.sessions[sessionID]; ok {
			sess.StartUpstream()
		}
	}

	return "ok", nil
}

// LookupTransport retrieves an existing transport for a substream.
func (remote *Remote) LookupTransport(streamName, substreamName, protocol, comType string) *Transport {
	remote.connMutex.Lock()
	stream, ok := remote.streams[streamName]
	remote.connMutex.Unlock()
	if !ok {
		return nil
	}
	for _, session := range stream.sessions {
		for e := session.Transports.Front(); e != nil; e = e.Next() {
			transport := e.Value.(*Transport)
			if transport.SubstreamName == substreamName && transport.Protocol == protocol && transport.ComType == comType {
				return transport
			}
		}
	}
	return nil
}

// Internal LookupStream for Remote's internal tracking
func (remote *Remote) lookupStreamInternal(streamName string) *Stream {
	remote.connMutex.Lock()
	defer remote.connMutex.Unlock()
	if stream, ok := remote.streams[streamName]; ok {
		return stream
	}
	stream := &Stream{
		Path:     streamName,
		sessions: make(map[string]*Session),
	}
	remote.streams[streamName] = stream
	return stream
}

// SendRequestSync sends an RTSP request to the remote server and waits for a synchronous response.
func (remote *Remote) SendRequestSync(request *Request) error {
	// 🔥 HIKVISION FIX: Делаем до 2 попыток. Если первая зависает (устаревший nonce),
	// очищаем nonce и пробуем снова, чтобы принудительно получить 401挑战.
	for attempt := 0; attempt < 2; attempt++ {
		ipc := NewIPC(10, 100)

		// Очищаем список подписчиков перед новой попыткой, чтобы не было утечек или дубликатов
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
			return err
		}

		result := ipc.GetResponse()

		// If server is shutting down, return immediately
		select {
		case <-remote.Server.ctx.Done():
			return errors.New("server is shutting down, request aborted")
		default:
			// continue
		}

		// Если произошел таймаут и это первая попытка
		if result == "timeout" && attempt == 0 {
			Logf("⚠️ [AUTH] Request timed out (camera silently dropped stale nonce). Clearing nonce and retrying...")
			remote.digest.Nonce = "" // Очищаем старый nonce
			request.Attempts = 0     // Сбрасываем счетчик попыток для корректной обработки 401
			continue                 // Переходим ко второй итерации цикла
		}

		if result != "ok" {
			return errors.New(result)
		}

		return nil // Успех
	}

	return errors.New("timeout after retry")
}

// SendRequest sends an RTSP request to the remote server.
func (remote *Remote) SendRequest(request *Request) error {
	select {
	case <-remote.Server.ctx.Done():
		return fmt.Errorf("server is shutting down")
	default:
		remote.connMutex.Lock()
		if remote.RemoteConn == nil {
			remote.connMutex.Unlock()
			err := remote.Dial()
			if err != nil {
				return err
			}
			remote.connMutex.Lock()
		}
		conn := remote.RemoteConn
		remote.connMutex.Unlock()

		remote.currentCSeq++
		request.Headers["CSeq"] = strconv.Itoa(remote.currentCSeq)
		remote.createAuthenticatorStr(request)

		rawRequest := []byte(request.String())
		Logf("📤 RAW REQUEST TO CAMERA [%s]:\n%s", remote.Host, string(rawRequest))

		conn.SetWriteDeadline(time.Now().Add(GlobalConfig.WriteTimeout))
		_, err := conn.Write(rawRequest)
		conn.SetWriteDeadline(time.Time{})
		if err != nil {
			remote.Disconnect()
			return fmt.Errorf("failed to write to remote: %w", err)
		}
		return nil
	}
}

// SendBinary forwards interleaved RTP/RTCP data from client to remote
func (remote *Remote) SendBinary(channel int, data []byte) error {
	remote.connMutex.Lock()
	if remote.RemoteConn == nil {
		remote.connMutex.Unlock()
		LogCriticalf("⚠️ SendBinary failed: remote connection is closed")
		return errors.New("remote connection is closed")
	}
	conn := remote.RemoteConn
	remote.connMutex.Unlock()

	header := make([]byte, 4)
	header[0] = '$'
	header[1] = byte(channel)
	header[2] = byte((len(data) & 0xFF00) >> 8)
	header[3] = byte(len(data) & 0xFF)

	conn.SetWriteDeadline(time.Now().Add(GlobalConfig.WriteTimeout))
	_, err := conn.Write(header)
	if err != nil {
		conn.SetWriteDeadline(time.Time{})
		LogCriticalf("⚠️ SendBinary header failed: %v", err)
		remote.Disconnect()
		return fmt.Errorf("failed to write binary header: %w", err)
	}
	_, err = conn.Write(data)
	conn.SetWriteDeadline(time.Time{})
	if err != nil {
		LogCriticalf("⚠️ SendBinary data failed: %v", err)
		remote.Disconnect()
		return fmt.Errorf("failed to write binary data: %w", err)
	} else {
		Logf("✅ Successfully sent binary data to camera on channel %d, len %d", channel, len(data))
	}
	return err
}

func (remote *Remote) handleAuthenticationFailure(paramsStr string) bool {
	if paramsStr == "" || remote.digest.Username == "" || remote.digest.Password == "" {
		return false
	}

	digestRegex := regexp.MustCompile(`Digest realm="([^"]+)", nonce="([^"]+)"`)
	basicRegex := regexp.MustCompile(`Basic realm="([^"]+)"`)

	if matches := digestRegex.FindStringSubmatch(paramsStr); len(matches) == 3 {
		remote.digest.Realm = matches[1]
		remote.digest.Nonce = matches[2] // 🔥 ОБЯЗАТЕЛЬНО обновляем Nonce каждый раз!
		Logf("✅ [AUTH] Updated Digest: Realm=%q, New Nonce=%q", remote.digest.Realm, remote.digest.Nonce)
		return true
	} else if matches := basicRegex.FindStringSubmatch(paramsStr); len(matches) == 2 {
		remote.digest.Realm = matches[1]
		return true
	}

	return false
}

func (remote *Remote) createAuthenticatorStr(request *Request) {
	if remote.digest.Realm != "" && remote.digest.Username != "" && remote.digest.Password != "" {
		var response string
		if remote.digest.Nonce != "" {
			URL := request.GetURL().String()
			response = remote.digest.ComputeResponse(request.Method, URL)
			request.Headers["Authorization"] = fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
				remote.digest.Username, remote.digest.Realm, remote.digest.Nonce, URL, response)
		} else {
			usernamePassword := fmt.Sprintf("%s:%s", remote.digest.Username, remote.digest.Password)
			response = base64.StdEncoding.EncodeToString([]byte(usernamePassword))
			request.Headers["Authorization"] = fmt.Sprintf("Basic %s", response)
		}
	}
}
