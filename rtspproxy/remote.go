package rtspproxy

import (
	"bytes"
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
	streams     map[string]*Stream
	interlayers map[int]*Interlayer
	requests    *list.List
	digest      *Digest
}

// LookupStream retrieves an existing stream or creates a new one if it doesn't exist.
func (remote *Remote) LookupStream(streamName string) *Stream {
	if stream, ok := remote.streams[streamName]; ok {
		return stream
	}
	stream := NewStream(remote, streamName)
	remote.streams[streamName] = stream
	return stream
}

// NewRemote creates a new Remote instance.
func NewRemote(server *Server, host, username, password string) *Remote {
	addr, err := net.ResolveTCPAddr("tcp", host)
	if err != nil {
		LogCriticalf("Failed to resolve TCP address: %s", err.Error())
		return nil
	}

	remote := &Remote{
		Host:        host,
		Server:      server,
		addr:        addr,
		streams:     make(map[string]*Stream),
		interlayers: make(map[int]*Interlayer),
		requests:    list.New(),
		digest:      NewDigest(),
	}
	if username != "" {
		remote.digest.Username = username
		remote.digest.Password = password
	}
	// Connect on-demand: Dial() is no longer called here.
	// It will be triggered by the first SendRequest.
	return remote
}

// Dial establishes a connection to the remote RTSP server.
func (remote *Remote) Dial() error {
	timeout := 5
	dialer := net.Dialer{Timeout: time.Duration(timeout) * time.Second}
	socket, err := dialer.Dial("tcp", remote.Host)
	if err != nil {
		LogCriticalf("Failed to connect to the remote server: %s", err.Error())
		return err
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

	handler := remote.incomingRequestHandler
	go handler()
	return nil
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
			for _, session := range stream.Sessions {
				session.Stop()
			}
			// Очищаем мапу сессий, чтобы не было утечек памяти
			stream.Sessions = make(map[string]*Session)
		}

		// 🔥 2. Закрываем сокет
		remote.RemoteConn.Close()
		remote.RemoteConn = nil

		// 🔥 3. Очищаем остальное состояние
		remote.digest.Nonce = ""
		remote.streams = make(map[string]*Stream)
		remote.interlayers = make(map[int]*Interlayer)
		if remote.requests != nil {
			remote.requests.Init()
		}
		remote.currentCSeq = 0

		LogCriticalf("Remote disconnected [%s]. All sessions stopped. State cleared. Will reconnect on demand.", remote.Host)
	}
}

// Destroy is for full cleanup (e.g., server shutdown)
func (remote *Remote) Destroy() error {
	remote.Server.RemoveRemote(remote.Host)
	for _, interlayers := range remote.interlayers {
		for _, session := range interlayers.Stream.Sessions {
			session.Stop()
		}
		for e := interlayers.Subscribers.Front(); e != nil; e = e.Next() {
			subscriber := e.Value.(*Subscriber)
			subscriber.Client.Destroy()
			interlayers.Subscribers.Remove(e)
		}
	}
	if remote.RemoteConn != nil {
		remote.RemoteConn.Close()
	}
	return nil
}

func (remote *Remote) handleStream(tcpChannel, length int, dataBuffer []byte) {
	interlayer := remote.interlayers[tcpChannel]
	if interlayer == nil {
		return
	}

	// Multi-client fanout: non-blocking write to each subscriber
	for e := interlayer.Subscribers.Front(); e != nil; e = e.Next() {
		subscriber := e.Value.(*Subscriber)
		hdr := make([]byte, 4)
		hdr[0] = '$'
		hdr[1] = byte(subscriber.Channel)
		hdr[2] = byte((length & 0xFF00) >> 8)
		hdr[3] = byte(length & 0xFF)

		packet := make([]byte, 4+len(dataBuffer))
		copy(packet, hdr)
		copy(packet[4:], dataBuffer)

		select {
		case subscriber.Client.writeChan <- packet:
		default:
			LogCriticalf("Client write channel full, dropping packet for client %s", subscriber.Client.remoteAddr)
		}
	}
}

func (remote *Remote) incomingRequestHandler() {
	defer func() {
		if re := recover(); re != nil {
			LogCriticalf("Remote Handle panic: %v", re)
		}
		LogCriticalf("disconnected the remote connection [%s:%s].", remote.remoteAddr, remote.remotePort)
		remote.Disconnect() // Auto-reconnect friendly
	}()

	buffer := make([]byte, rtspBufferSize)
	length := 0

	for {
		recvLen, err := remote.RemoteConn.Read(buffer[length:])
		if err != nil {
			return
		}
		length += recvLen

		if buffer[0] == '$' {
			for length < streamHeaderLength {
				recvLen, err := remote.RemoteConn.Read(buffer[length:])
				if err != nil {
					return
				}
				length += recvLen
			}

			tcpChannel := int(buffer[1])
			streamDataLength := ((int(buffer[2]) << 8) | int(buffer[3]))
			streamDataRecvLength := length - streamHeaderLength

			for streamDataRecvLength < streamDataLength {
				recvLen, err := remote.RemoteConn.Read(buffer[length:])
				if err != nil {
					return
				}
				length += recvLen
				streamDataRecvLength = length - streamHeaderLength
			}

			dataBuffer := make([]byte, streamDataLength)
			copy(dataBuffer, buffer[streamHeaderLength:streamHeaderLength+streamDataLength])
			length = copy(buffer, buffer[streamHeaderLength+streamDataLength:length])

			// 🔥 НОВЫЙ ЛОГ: Показывает, что камера реально шлет данные
			if tcpChannel == 0 { // 0 = RTP Video, 1 = RTCP Video
				Logf("📦 [MEDIA] Received RTP video packet from camera, len: %d", streamDataLength)
			}

			remote.handleStream(tcpChannel, streamDataLength, dataBuffer)
		} else {
			eol := bytes.Index(buffer, []byte("$"))
			if eol == -1 {
				eol = length
			}

			recv := string(buffer[:eol])
			if eol > 0 && eol != length {
				length = copy(buffer, buffer[eol:length])
			} else {
				length = 0
			}

			// 🔥 ДЕТАЛЬНОЕ ЛОГИРОВАНИЕ СЫРОГО ОТВЕТА ОТ КАМЕРЫ
			Logf("📥 RAW RESPONSE FROM CAMERA [%s]:\n%s", remote.Host, recv)

			response, err := NewResponseFromBuffer(recv)
			if err != nil {
				LogCriticalf("remote rtsp read request error: %v", err)
				return
			}

			requestEl := remote.requests.Front()
			if requestEl == nil {
				LogCriticalf("⚠️ [QUEUE] Received response but queue is empty! Dropping.")
				length = 0
				continue
			}

			request := requestEl.Value.(*Request)

			// 🔥 ВАЖНО: НЕ удаляем запрос из очереди сразу!
			// Если это 401 и мы будем делать retry, запрос должен остаться в очереди,
			// чтобы следующий ответ (200 OK) был сопоставлен с этим же запросом.

			status := "ok"
			if response.Code == 401 && request.Attempts == 0 {
				if wwwAuthenticate, ok := response.Headers["WWW-Authenticate"]; ok {
					if remote.digest.Username != "" && remote.digest.Password != "" && remote.handleAuthenticationFailure(wwwAuthenticate) {
						request.Attempts++
						Logf("🔑 [AUTH] Retrying with Digest auth (CSeq will be updated)...")
						remote.SendRequest(request)
						length = 0
						continue // Возвращаемся в начало цикла, чтобы прочитать ОТВЕТ на retry. Запрос всё еще в очереди!
					} else {
						LogCriticalf("❌ [AUTH] Auth failed or missing credentials.")
						status = "unauthorized"
					}
				}
			} else {
				// 🔥 ПРОВЕРКА ОШИБОК: Если камера вернула 4xx или 5xx, считаем это ошибкой
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

			// 🔥 ТЕПЕРЬ удаляем запрос из очереди, так как он полностью обработан
			remote.requests.Remove(requestEl)

			// Разблокируем ожидающий IPC-канал
			// 🔥 БЕЗОПАСНАЯ ОТПРАВКА: защищаемся от паники "send on closed channel"
			for e := request.Subscriptions.Front(); e != nil; e = e.Next() {
				ch := e.Value.(chan string)
				func() {
					defer func() {
						if r := recover(); r != nil {
							// Клиент уже отключился и закрыл канал, это нормально, просто игнорируем
							Logf("⚠️ [IPC] Client disconnected before response (recovered from panic)")
						}
					}()
					ch <- status
				}()
				request.Subscriptions.Remove(e)
			}
		}
	}
}

func (remote *Remote) handleTeardown(request *Request, response *Response) {
	streamName := request.URL.Path
	stream := remote.LookupStream(streamName)
	session := stream.LookupSession(request.Headers["Session"])
	session.Stop()
	for e := session.Transports.Front(); e != nil; e = e.Next() {
		transport := e.Value.(*Transport)
		delete(remote.interlayers, transport.Substreams[0].Channel)
		delete(remote.interlayers, transport.Substreams[1].Channel)
	}
	delete(stream.Sessions, request.Headers["Session"])
	if len(remote.interlayers) == 0 {
		Logf("no active sessions. closing connection")
		remote.Disconnect()
	}
}

func (remote *Remote) handleOptions(request *Request, response *Response) {
	streamName := request.URL.Path
	stream := remote.LookupStream(streamName)
	stream.Options = response.Headers["Public"]
	stream.Server = response.Headers["Server"]
}

func (remote *Remote) handleDescribe(request *Request, response *Response) {
	streamName := request.URL.Path
	stream := remote.LookupStream(streamName)
	stream.SDP = response.Body
}

func (remote *Remote) parseTransport(transportStr string) (string, string, map[string]string) {
	transportParts := strings.Split(transportStr, ";")
	protocol := transportParts[0]
	comType := "unicast"
	if len(transportParts) > 1 {
		comType = transportParts[1]
	}
	transportParts = transportParts[2:]
	params := make(map[string]string)
	for _, element := range transportParts {
		kv := strings.Split(element, "=")
		if len(kv) == 2 {
			params[kv[0]] = kv[1]
		}
	}
	return protocol, comType, params
}

func (remote *Remote) handleSetup(request *Request, response *Response) {
	streamName, substreamName := filepath.Split(request.URL.Path)
	streamName = filepath.Dir(streamName)
	protocol, comType, params := remote.parseTransport(response.Headers["Transport"])
	stream := remote.LookupStream(streamName)
	sessionParams := strings.Split(response.Headers["Session"], ";")
	session := stream.LookupSession(sessionParams[0])
	transport := session.LookupTransport(substreamName, protocol, comType)
	transport.Ssrc = params["ssrc"]

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
	stream := remote.LookupStream(streamName)
	rtpInfo := response.Headers["RTP-Info"]
	session := stream.LookupSession(request.Headers["Session"])
	transports := session.Transports

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
	session.Start()
}

// GetOptions retrieves the OPTIONS response for a given stream.
func (remote *Remote) GetOptions(streamName string) (string, error) {
	stream := remote.LookupStream(streamName)
	if stream.Options == "" {
		// 🔥 ПРИНУДИТЕЛЬНО ИСПОЛЬЗУЕМ КОРЕНЬ "/" ДЛЯ HIKVISION
		URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: "/"}
		request, _ := NewRequest("OPTIONS", URL)
		err := remote.SendRequestSync(request)
		if err != nil {
			return "", err
		}
	}
	return stream.Options, nil
}

// GetSDP retrieves the SDP description for a given stream.
func (remote *Remote) GetSDP(streamName string) (string, error) {
	stream := remote.LookupStream(streamName)
	if stream.SDP == "" {
		URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: streamName}
		request, _ := NewRequest("DESCRIBE", URL)
		err := remote.SendRequestSync(request)
		if err != nil {
			return "", err
		}
	}
	return stream.SDP, nil
}

// GetSsrcSession retrieves the SSRC and session ID for a given stream and substream.
func (remote *Remote) GetSsrcSession(client *Client, streamName, substreamName, transportStr string) (string, string, error) {
	protocol, comType, _ := remote.parseTransport(transportStr)
	stream := remote.LookupStream(streamName)

	// Проверяем, есть ли уже настроенный транспорт для этого подпотока
	transport := stream.LookupTransport(substreamName, protocol, comType)

	if transport == nil {
		// ПЕРВЫЙ КЛИЕНТ: Настраиваем с камерой (ваш существующий код)
		index := len(remote.interlayers)
		if protocol == "RTP/AVP/TCP" {
			URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: streamName + "/" + substreamName}
			request, _ := NewRequest("SETUP", URL)
			request.Headers["Transport"] = fmt.Sprintf("%s;%s;interleaved=%d-%d", protocol, comType, index, index+1)
			err := remote.SendRequestSync(request)
			if err != nil {
				return "", "", err
			}
			transport = stream.LookupTransport(substreamName, protocol, comType)
			if transport == nil {
				return "", "", errors.New("could not create transport")
			}
			transport.Substreams[0] = NewSubstream(transport, substreamName)
			transport.Substreams[0].Channel = index
			transport.Substreams[1] = NewSubstream(transport, substreamName)
			transport.Substreams[1].Channel = index + 1
			remote.interlayers[index] = NewInterlayer(index, stream, transport, transport.Substreams[0])
			remote.interlayers[index+1] = NewInterlayer(index, stream, transport, transport.Substreams[1])

			// 🔥 КРИТИЧЕСКИ ВАЖНО: Добавляем ПЕРВОГО клиента в список рассылки!
			remote.interlayers[index].Subscribers.PushBack(NewSubscriber(client, index))
			remote.interlayers[index+1].Subscribers.PushBack(NewSubscriber(client, index+1))
		} else {
			return "", "", fmt.Errorf("unsupported protocol '%s'", protocol)
		}
	} else {
		// 🔥 ВТОРОЙ И ПОСЛЕДУЮЩИЕ КЛИЕНТЫ: Переиспользуем существующую сессию!
		Logf("✅ Reusing existing transport for %s, adding client to fanout", substreamName)

		// Безопасно добавляем клиента во ВСЕ interlayers, связанные с этим транспортом
		// (обычно это RTP и RTCP каналы, но мы не предполагаем жесткие номера каналов)
		added := false
		for ch, interlayer := range remote.interlayers {
			if interlayer.Transport == transport {
				interlayer.Subscribers.PushBack(NewSubscriber(client, ch))
				added = true
			}
		}

		if !added {
			return "", "", errors.New("could not find existing interlayer for transport")
		}

		// 🔥 ЗАЩИТА ОТ NIL: Гарантируем, что Session существует перед возвратом
		if transport.Session == nil {
			return "", "", errors.New("transport session is nil")
		}
	}

	return transport.Ssrc, transport.Session.Session, nil
}

// Unsubscribe removes a client from all active interlayers.
func (remote *Remote) Unsubscribe(client *Client) {
	hasSubscribers := false

	for _, interlayers := range remote.interlayers {
		for e := interlayers.Subscribers.Front(); e != nil; e = e.Next() {
			if e.Value.(*Subscriber).Client == client {
				interlayers.Subscribers.Remove(e)
				break
			}
		}
		if interlayers.Subscribers.Len() > 0 {
			hasSubscribers = true
		}
	}

	// Если подписчиков не осталось, немедленно и чисто разрываем соединение
	if !hasSubscribers {
		Logf("No more subscribers for %s. Triggering clean disconnect.", remote.Host)
		remote.Disconnect()
	}
}

// GetRTPInfo retrieves RTP information for a given stream and session.
func (remote *Remote) GetRTPInfo(streamName, sessionID string) (string, error) {
	stream := remote.LookupStream(streamName)
	session := stream.LookupSession(sessionID)
	if session == nil {
		return "", errors.New("session not found")
	}

	transports := session.Transports
	if transports.Len() == 0 {
		return "", errors.New("no streams were setup")
	}

	// 🔥 УМНАЯ ПРОВЕРКА: Если Seq == 0, значит PLAY еще не отправлялся (первый клиент).
	// Отправляем PLAY камере, чтобы она начала стриминг.
	transport := transports.Front().Value.(*Transport)
	if transport.Substreams[0].Channel >= 0 && transport.Substreams[0].Seq == 0 {
		URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: streamName}
		request, _ := NewRequest("PLAY", URL)
		request.Headers["Session"] = sessionID
		request.Headers["Range"] = "npt=0.000-"

		err := remote.SendRequestSync(request)
		if err != nil {
			return "", err
		}
	}

	// Генерируем RTP-Info на основе актуальных данных сессии
	parts := make([]string, session.Transports.Len())
	i := 0
	for e := session.Transports.Front(); e != nil; e = e.Next() {
		transport := e.Value.(*Transport)
		URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: streamName + "/" + transport.SubstreamName}
		parts[i] = fmt.Sprintf("url=%s;seq=%d;rtptime=%d", URL.String(), transport.Substreams[0].Seq, transport.Substreams[0].RTPTime)
		i++
	}
	return strings.Join(parts, ","), nil
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
	remote.connMutex.Lock()
	if remote.RemoteConn == nil {
		err := remote.Dial()
		if err != nil {
			remote.connMutex.Unlock()
			return err
		}
	}
	conn := remote.RemoteConn
	remote.connMutex.Unlock()

	remote.currentCSeq++
	request.Headers["CSeq"] = strconv.Itoa(remote.currentCSeq)

	// 🔥 ОТЛАДКА: Печатаем состояние аутентификации прямо перед отправкой
	Logf("🔍 [AUTH STATE] Method: %s, User: %q, Pass: %q, Realm: %q, Nonce: %q",
		request.Method, remote.digest.Username, remote.digest.Password, remote.digest.Realm, remote.digest.Nonce)

	remote.createAuthenticatorStr(request)

	rawRequest := []byte(request.String())
	Logf("📤 RAW REQUEST TO CAMERA [%s]:\n%s", remote.Host, string(rawRequest))

	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := conn.Write(rawRequest)
	if err != nil {
		LogCriticalf("Failed to write to remote: %v", err)
		remote.Disconnect()
		return err
	}
	conn.SetWriteDeadline(time.Time{})
	return nil
}

// SendBinary forwards interleaved RTP/RTCP data from client to remote
func (remote *Remote) SendBinary(channel int, data []byte) error {
	remote.connMutex.Lock()
	if remote.RemoteConn == nil {
		remote.connMutex.Unlock()
		return errors.New("remote connection is closed")
	}
	conn := remote.RemoteConn
	remote.connMutex.Unlock()

	header := make([]byte, 4)
	header[0] = '$'
	header[1] = byte(channel)
	header[2] = byte((len(data) & 0xFF00) >> 8)
	header[3] = byte(len(data) & 0xFF)

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, err := conn.Write(header)
	if err != nil {
		conn.SetWriteDeadline(time.Time{})
		remote.Disconnect()
		return err
	}
	_, err = conn.Write(data)
	conn.SetWriteDeadline(time.Time{})
	if err != nil {
		remote.Disconnect()
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
