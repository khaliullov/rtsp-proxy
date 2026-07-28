package rtspproxy

import (
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const rtspBufferSize = 65536

// streamHeaderLength is the length of the interleaved binary data header.
const streamHeaderLength = 4

// Client represents a client connection to the RTSP proxy.
type Client struct {
	ClientConn     net.Conn
	localPort      string
	remotePort     string
	localAddr      string
	remoteAddr     string
	currentCSeq    string
	responseBuffer string
	host           string
	basePath       string // 🔥 ДОБАВИТЬ: Базовый путь потока
	username       string
	password       string
	server         *Server
	writeChan      chan []byte
	currentStream  *Stream
	wg             sync.WaitGroup
}

// NewClient creates a new Client instance.
func NewClient(server *Server, socket net.Conn) *Client {
	localAddr := strings.Split(socket.LocalAddr().String(), ":")
	remoteAddr := strings.Split(socket.RemoteAddr().String(), ":")
	client := &Client{
		server:     server,
		ClientConn: socket,
		localAddr:  localAddr[0],
		localPort:  localAddr[1],
		remoteAddr: remoteAddr[0],
		remotePort: remoteAddr[1],
		writeChan:  make(chan []byte, 200),
	}
	client.wg.Add(1)
	go client.writer()

	LogCriticalf("accepted the client connection [%s:%s].", client.remoteAddr, client.remotePort)
	return client
}

func (client *Client) writer() {
	defer client.wg.Done()
	for {
		select {
		case <-client.server.ctx.Done():
			Logf("Client writer for [%s:%s] stopping due to server shutdown.", client.remoteAddr, client.remotePort)
			return
		case data, ok := <-client.writeChan:
			if !ok {
				Logf("Client writer for [%s:%s] stopping, write channel closed.", client.remoteAddr, client.remotePort)
				return
			}
			client.ClientConn.SetWriteDeadline(time.Now().Add(GlobalConfig.WriteTimeout))
			_, err := client.ClientConn.Write(data)
			client.ClientConn.SetWriteDeadline(time.Time{})
			if err != nil {
				LogCriticalf("Client write error [%s:%s]: %v", client.remoteAddr, client.remotePort, err)
				return
			}
		}
	}
}

// Destroy closes the client connection and cleans up resources.
func (client *Client) Destroy() error {
	Logf("Destroying client connection [%s:%s].", client.remoteAddr, client.remotePort)
	client.ClientConn.Close() // Unblock writer and reader
	close(client.writeChan)
	client.wg.Wait()
	return nil
}

func (client *Client) incomingRequestHandler() {
	defer func() {
		LogCriticalf("disconnected the client connection [%s:%s].", client.remoteAddr, client.remotePort)
		if client.currentStream != nil {
			client.currentStream.RemoveClient(client)
		}
		client.Destroy()
	}()

	buffer := make([]byte, rtspBufferSize)
	length := 0

	for {
		select {
		case <-client.server.ctx.Done():
			Logf("Client reader for [%s:%s] stopping due to server shutdown.", client.remoteAddr, client.remotePort)
			return
		default:
			client.ClientConn.SetReadDeadline(time.Now().Add(GlobalConfig.ReadTimeout))
			recvLen, err := client.ClientConn.Read(buffer[length:])
			client.ClientConn.SetReadDeadline(time.Time{}) // Clear deadline

			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue // Timeout, check context again
				}
				if err.Error() != "EOF" {
					LogCriticalf("Client read error [%s:%s]: %v", client.remoteAddr, client.remotePort, err)
				}
				return
			}
			length += recvLen
			if length == 0 {
				continue
			}

			if buffer[0] == '$' {
				// Process interleaved binary data (RTP/RTCP from client)
				for length < streamHeaderLength {
					select {
					case <-client.server.ctx.Done():
						Logf("Client reader for [%s:%s] stopping during stream header read due to server shutdown.", client.remoteAddr, client.remotePort)
						return
					default:
						client.ClientConn.SetReadDeadline(time.Now().Add(GlobalConfig.ReadTimeout))
						recvLen, err := client.ClientConn.Read(buffer[length:])
						client.ClientConn.SetReadDeadline(time.Time{})
						if err != nil {
							if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
								continue
							}
							LogCriticalf("Client read error during stream header [%s:%s]: %v", client.remoteAddr, client.remotePort, err)
							return
						}
						length += recvLen
					}
				}

				tcpChannel := int(buffer[1])
				streamDataLength := ((int(buffer[2]) << 8) | int(buffer[3]))
				streamDataRecvLength := length - streamHeaderLength

				for streamDataRecvLength < streamDataLength {
					select {
					case <-client.server.ctx.Done():
						Logf("Client reader for [%s:%s] stopping during stream data read due to server shutdown.", client.remoteAddr, client.remotePort)
						return
					default:
						client.ClientConn.SetReadDeadline(time.Now().Add(GlobalConfig.ReadTimeout))
						recvLen, err := client.ClientConn.Read(buffer[length:])
						client.ClientConn.SetReadDeadline(time.Time{})
						if err != nil {
							if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
								continue
							}
							LogCriticalf("Client read error during stream data [%s:%s]: %v", client.remoteAddr, client.remotePort, err)
							return
						}
						length += recvLen
						streamDataRecvLength = length - streamHeaderLength
					}
				}

				dataBuffer := make([]byte, streamDataLength)
				copy(dataBuffer, buffer[streamHeaderLength:streamHeaderLength+streamDataLength])
				length = copy(buffer, buffer[streamHeaderLength+streamDataLength:length])

				if client.currentStream != nil && client.currentStream.remote != nil {
					remote := client.currentStream.remote
					// We need to map client channel back to upstream channel
					// We can find this in ClientSession
					upstreamChannel := tcpChannel
					client.currentStream.mu.RLock()
					if cs, ok := client.currentStream.clients[client]; ok {
						for u, c := range cs.channels {
							if c == tcpChannel {
								upstreamChannel = u
								break
							}
						}
					}
					client.currentStream.mu.RUnlock()

					Logf("📥 Received binary data from client on channel %d, forwarding to remote channel %d, len %d", tcpChannel, upstreamChannel, streamDataLength)
					go remote.SendBinary(upstreamChannel, dataBuffer)
				}
				continue
			}

			reqStr := string(buffer[:length])
			length = 0

			// 🔥 ДЕТАЛЬНОЕ ЛОГИРОВАНИЕ СЫРОГО ЗАПРОСА
			Logf("📩 RAW REQUEST from [%s:%s]:\n%s", client.remoteAddr, client.remotePort, reqStr)

			request, err := NewRequestFromBuffer(reqStr)
			if err != nil {
				LogCriticalf("❌ Failed to parse request: %v", err)
				return
			}
			Logf("DEBUG: Client received request with URL: %+v", request.URL)

			if client.host == "" {
				client.username = request.URL.User.Username()
				client.password, _ = request.URL.User.Password()

				trimmedPath := strings.TrimPrefix(request.URL.Path, "/")
				parts := strings.SplitN(trimmedPath, "/", 3)

				if len(parts) >= 2 && parts[0] == "rtsp" {
					client.host = parts[1]
					if len(parts) == 3 {
						request.URL.Path = "/" + parts[2]
					} else {
						request.URL.Path = "/"
					}
				} else {
					if request.Method == "OPTIONS" && (request.URL.Path == "*" || request.URL.Path == "/" || request.URL.Path == "") {
						LogCriticalf("Client probing proxy directly. Responding with proxy capabilities.")
						response, _ := NewResponse(200, "OK")
						response.Headers["Public"] = "OPTIONS, DESCRIBE, SETUP, PLAY, TEARDOWN, GET_PARAMETER"
						response.Headers["Server"] = "RTSP-Proxy/1.0"
						response.Headers["CSeq"] = client.getHeader(request, "CSeq")
						client.ClientConn.Write([]byte(response.String()))
						return
					}
					client.host = request.URL.Host
				}

				if strings.Contains(client.host, "@") {
					authAndHost := strings.SplitN(client.host, "@", 2)
					client.host = authAndHost[1]
					authParts := strings.SplitN(authAndHost[0], ":", 2)
					if len(authParts) == 2 {
						client.username = authParts[0]
						client.password = authParts[1]
					}
				}

				// 🔥 КРИТИЧЕСКИ ВАЖНО: Запоминаем базовый путь при первом запросе!
				client.basePath = request.URL.Path

				LogCriticalf("✅ Resolved client target: host=%s, path=%s, user=%s", client.host, client.basePath, client.username)
			}

			// 🔥 ИСПОЛЬЗУЕМ basePath для поиска потока, а не request.URL.Path
			stream := client.server.LookupStream(client.host, client.username, client.password, client.basePath)
			client.currentStream = stream
			if stream == nil {
				LogCriticalf("❌ Failed to create or find stream for host: %s", client.host)
				response := client.responseNotFound(request)
				client.ClientConn.Write([]byte(response.String()))
				return
			}

			response := client.responseBadRequest(request)
			switch request.Method {
			case "OPTIONS":
				response = client.handleOptions(stream, request)
			case "DESCRIBE":
				response = client.handleDescribe(stream, request)
			case "SETUP":
				transport := client.getHeader(request, "Transport")
				if transport != "" && strings.Contains(transport, "RTP/AVP") && !strings.Contains(transport, "RTP/AVP/TCP") {
					LogCriticalf("⚠️ Client requested UDP (%s), but proxy only supports TCP. Sending 461 Unsupported Transport.", transport)
					response = client.responseUnsupportedTransport(request)
				} else {
					response = client.handleSetup(stream, request)
				}
			case "PLAY":
				response = client.handlePlay(stream, request)
			case "TEARDOWN":
				response = client.handleTeardown(stream, request)
			case "GET_PARAMETER":
				response = client.handleGetParameter(stream, request)
			}

			response.Headers["Via"] = "RTSP-Proxy"
			cseq := client.getHeader(request, "CSeq")
			if cseq != "" {
				response.Headers["CSeq"] = cseq
			}

			// 🛡️ ИСПРАВЛЕНИЕ: VLC не любит пустой заголовок Server
			if server, ok := response.Headers["Server"]; !ok || server == "" {
				response.Headers["Server"] = "RTSP-Proxy/1.0"
			}

			// 🔥 ДЕТАЛЬНОЕ ЛОГИРОВАНИЕ СЫРОГО ОТВЕТА
			respStr := response.String()
			Logf("📤 RAW RESPONSE to [%s:%s]:\n%s", client.remoteAddr, client.remotePort, respStr)

			client.ClientConn.Write([]byte(respStr))
		}
	}
}

func (client *Client) responseUnsupportedTransport(request *Request) *Response {
	response, _ := NewResponse(461, "Unsupported Transport")
	// Явно указываем клиенту, что мы поддерживаем только TCP Interleaved
	response.Headers["Transport"] = "RTP/AVP/TCP;unicast;interleaved=0-1"
	response.Headers["Supported"] = "RTP/AVP/TCP"
	return response
}

func (client *Client) getHeader(request *Request, key string) string {
	if value, ok := request.Headers[key]; ok {
		return value
	}
	return ""
}

func (client *Client) responseNotFound(request *Request) *Response {
	response, _ := NewResponse(404, "Stream Not Found")
	return response
}

func (client *Client) responseBadRequest(request *Request) *Response {
	response, _ := NewResponse(400, "Bad Request")
	return response
}

func (client *Client) responseUnauthorized(request *Request) *Response {
	response, _ := NewResponse(401, "Unauthorized")
	return response
}

func (client *Client) handleGetParameter(stream *Stream, request *Request) *Response {
	session := request.Headers["Session"]
	response, _ := NewResponse(200, "OK")
	response.Headers["Session"] = session
	response.Headers["Server"] = stream.Server
	return response
}

func (client *Client) handleOptions(stream *Stream, request *Request) *Response {
	// If stream is not connected yet, try to connect to get options
	if stream.GetOptions() == "" {
		stream.Start()
		// Wait a bit for connection or just return default
		select {
		case <-client.server.ctx.Done():
		case <-time.After(500 * time.Millisecond):
		}
	}

	response, _ := NewResponse(200, "OK")
	opts := stream.GetOptions()
	if opts != "" {
		response.Headers["Public"] = opts
	} else {
		response.Headers["Public"] = "OPTIONS, DESCRIBE, SETUP, PLAY, TEARDOWN, GET_PARAMETER"
	}
	response.Headers["Server"] = stream.Server
	return response
}

func (client *Client) handleSetup(stream *Stream, request *Request) *Response {
	_, substreamName := filepath.Split(request.GetURL().Path)
	transport := client.getHeader(request, "Transport")

	protocol, comType, params := stream.remote.parseTransport(transport)

	// Гарантируем, что процесс подключения запущен
	stream.Start()

	// 🔥 Ждем, пока connectLoop завершит OPTIONS, DESCRIBE, SETUP и PLAY (StatePlaying)
	for i := 0; i < 100; i++ { // до 10 секунд
		select {
		case <-client.server.ctx.Done():
			return client.responseBadRequest(request)
		default:
			st := stream.GetState()
			if st == StatePlaying {
				goto ready
			}
			if st == StateDisconnected || st == StateStopping {
				LogCriticalf("❌ [SETUP] Stream disconnected during setup")
				return client.responseBadRequest(request)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	if stream.GetState() != StatePlaying {
		LogCriticalf("❌ [SETUP] Timeout waiting for stream to start")
		return client.responseBadRequest(request)
	}

ready:

	upstreamTransport := stream.LookupTransport(substreamName, protocol, comType)
	if upstreamTransport == nil {
		LogCriticalf("❌ [SETUP] Failed to find upstream transport for %s", substreamName)
		return client.responseBadRequest(request)
	}

	sessionID := upstreamTransport.Session.Session

	// 🔥 КРИТИЧЕСКИ ВАЖНО: Добавляем клиента в поток ЗДЕСЬ, чтобы MapChannel сработал!
	stream.AddClient(client, sessionID)

	response, _ := NewResponse(200, "OK")
	proxyIP := client.localAddr
	if proxyIP == "0.0.0.0" {
		proxyIP = "127.0.0.1"
	}

	// Interleaved channels for client
	clientInterleaved := params["interleaved"]
	if clientInterleaved != "" {
		channels := strings.Split(clientInterleaved, "-")
		ch1, _ := strconv.Atoi(channels[0])

		// Теперь MapChannel найдет клиента в s.clients и корректно сохранит маппинг!
		stream.MapChannel(client, upstreamTransport.Substreams[0].Channel, ch1)
		if len(channels) > 1 {
			ch2, _ := strconv.Atoi(channels[1])
			stream.MapChannel(client, upstreamTransport.Substreams[1].Channel, ch2)
		}
	}

	cleanTransport := regexp.MustCompile(`;?(destination|source)=[^;]+`).ReplaceAllString(transport, "")
	response.Headers["Transport"] = fmt.Sprintf("%s;ssrc=%s;destination=%s;source=%s", cleanTransport, upstreamTransport.Ssrc, client.remoteAddr, proxyIP)
	response.Headers["Cache-Control"] = "must-revalidate"
	response.Headers["Session"] = sessionID + ";timeout=60"
	response.Headers["Server"] = stream.Server
	return response
}

func (client *Client) handleDescribe(stream *Stream, request *Request) *Response {
	stream.Start()

	// Wait for SDP to be available (increased timeout to 10s)
	for i := 0; i < 100; i++ {
		select {
		case <-client.server.ctx.Done():
			return client.responseBadRequest(request)
		default:
			if stream.GetSDP() != "" {
				goto sdpReady
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	if stream.GetSDP() == "" {
		LogCriticalf("❌ [DESCRIBE] Failed to get SDP for %s (timeout or error)", stream.Path)
		return client.responseBadRequest(request)
	}

sdpReady:

	response, _ := NewResponse(200, "OK")
	response.Headers["Content-Type"] = "application/sdp"
	response.Headers["Server"] = stream.Server

	proxyIP := client.localAddr
	if proxyIP == "0.0.0.0" || proxyIP == "127.0.0.1" {
		proxyIP = "127.0.0.1"
	}

	rewrittenSDP := strings.ReplaceAll(stream.GetSDP(), "0.0.0.0", proxyIP)

	response.Headers["Content-Length"] = strconv.Itoa(len(rewrittenSDP))
	response.Body = rewrittenSDP
	return response
}

func (client *Client) handlePlay(stream *Stream, request *Request) *Response {
	sessionID := request.Headers["Session"]

	response, _ := NewResponse(200, "OK")
	response.Headers["Range"] = request.Headers["Range"]
	response.Headers["Session"] = sessionID
	response.Headers["Server"] = stream.Server

	proxyIP := client.localAddr
	if proxyIP == "0.0.0.0" || proxyIP == "127.0.0.1" {
		proxyIP = "127.0.0.1"
	}

	// 🔥 ИСПРАВЛЕНИЕ: Избегаем двойного слеша (//) в URL
	parts := []string{}
	parts = append(parts, fmt.Sprintf("url=rtsp://%s%s;seq=0;rtptime=0", proxyIP, stream.Path))

	response.Headers["RTP-Info"] = strings.Join(parts, ",")

	return response
}

func (client *Client) handleTeardown(stream *Stream, request *Request) *Response {
	stream.RemoveClient(client)
	response, _ := NewResponse(200, "OK")
	response.Headers["Session"] = request.Headers["Session"]
	response.Headers["Server"] = stream.Server
	return response
}
