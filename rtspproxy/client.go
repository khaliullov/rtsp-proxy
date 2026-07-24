package rtspproxy

import (
	"fmt"
	"log"
	"net"
	"net/url"
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
	username       string
	password       string
	server         *Server
	writeChan      chan []byte
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

	log.Printf("accepted the client connection [%s:%s].", client.remoteAddr, client.remotePort)
	return client
}

func (client *Client) writer() {
	defer client.wg.Done()
	for data := range client.writeChan {
		client.ClientConn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_, err := client.ClientConn.Write(data)
		client.ClientConn.SetWriteDeadline(time.Time{})
		if err != nil {
			return
		}
	}
}

// Destroy closes the client connection and cleans up resources.
func (client *Client) Destroy() error {
	client.ClientConn.Close() // Unblock writer and reader
	close(client.writeChan)
	client.wg.Wait()
	return nil
}

func (client *Client) incomingRequestHandler() {
	defer func() {
		log.Printf("disconnected the client connection [%s:%s].", client.remoteAddr, client.remotePort)
		client.Destroy()
		if client.host != "" {
			remote := client.server.LookupRemote(client.host, client.username, client.password)
			if remote != nil {
				remote.Unsubscribe(client)
			}
		}
	}()

	buffer := make([]byte, rtspBufferSize)
	length := 0

	for {
		recvLen, err := client.ClientConn.Read(buffer[length:])
		if err != nil {
			if err.Error() != "EOF" {
				log.Printf("Client read error [%s:%s]: %v", client.remoteAddr, client.remotePort, err)
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
				recvLen, err := client.ClientConn.Read(buffer[length:])
				if err != nil {
					return
				}
				length += recvLen
			}

			tcpChannel := int(buffer[1])
			streamDataLength := ((int(buffer[2]) << 8) | int(buffer[3]))
			streamDataRecvLength := length - streamHeaderLength

			for streamDataRecvLength < streamDataLength {
				recvLen, err := client.ClientConn.Read(buffer[length:])
				if err != nil {
					return
				}
				length += recvLen
				streamDataRecvLength = length - streamHeaderLength
			}

			dataBuffer := make([]byte, streamDataLength)
			copy(dataBuffer, buffer[streamHeaderLength:streamHeaderLength+streamDataLength])
			length = copy(buffer, buffer[streamHeaderLength+streamDataLength:length])

			if client.host != "" {
				remote := client.server.LookupRemote(client.host, client.username, client.password)
				if remote != nil {
					var remoteChannel int = tcpChannel
					for ch, interlayer := range remote.interlayers {
						for e := interlayer.Subscribers.Front(); e != nil; e = e.Next() {
							if e.Value.(*Subscriber).Client == client && e.Value.(*Subscriber).Channel == tcpChannel {
								remoteChannel = ch
								break
							}
						}
					}
					go remote.SendBinary(remoteChannel, dataBuffer)
				}
			}
			continue
		}

		reqStr := string(buffer[:length])
		length = 0

		// 🔥 ДЕТАЛЬНОЕ ЛОГИРОВАНИЕ СЫРОГО ЗАПРОСА
		log.Printf("📩 RAW REQUEST from [%s:%s]:\n%s", client.remoteAddr, client.remotePort, reqStr)

		request, err := NewRequestFromBuffer(reqStr)
		if err != nil {
			log.Printf("❌ Failed to parse request: %v", err)
			return
		}

		if client.host == "" {
			client.username = request.URL.User.Username()
			client.password, _ = request.URL.User.Password()

			trimmedPath := strings.TrimPrefix(request.URL.Path, "/")
			parts := strings.SplitN(trimmedPath, "/", 3)

			if len(parts) >= 2 {
				if parts[0] == "rtsp" {
					client.host = parts[1]
					if len(parts) == 3 {
						request.URL.Path = "/" + parts[2]
					} else {
						request.URL.Path = "/"
					}
				} else if strings.Contains(parts[0], ":") {
					client.host = parts[0]
					if len(parts) >= 2 {
						request.URL.Path = "/" + parts[1]
						if len(parts) == 3 {
							request.URL.Path += "/" + parts[2]
						}
					} else {
						request.URL.Path = "/"
					}
				} else {
					client.host = request.URL.Host
				}
			} else {
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

			log.Printf("✅ Resolved client target: host=%s, path=%s, user=%s", client.host, request.URL.Path, client.username)
		}

		remote := client.server.LookupRemote(client.host, client.username, client.password)

		// 🔥 ГАРАНТИЯ АУТЕНТИФИКАЦИИ:
		// Принудительно обновляем учетные данные в объекте Remote,
		// чтобы механизм Digest-аутентификации точно сработал.
		if client.username != "" && client.password != "" {
			remote.digest.Username = client.username
			remote.digest.Password = client.password
		}

		if remote == nil {
			log.Printf("❌ Failed to create or find remote for host: %s", client.host)
			response := client.responseNotFound(request)
			client.ClientConn.Write([]byte(response.String()))
			return
		}

		response := client.responseBadRequest(request)
		switch request.Method {
		case "OPTIONS":
			response = client.handleOptions(remote, request)
		case "DESCRIBE":
			response = client.handleDescribe(remote, request)
		case "SETUP":
			transport := client.getHeader(request, "Transport")
			if transport != "" && strings.Contains(transport, "RTP/AVP") && !strings.Contains(transport, "RTP/AVP/TCP") {
				log.Printf("⚠️ Client requested UDP (%s), but proxy only supports TCP. Sending 461 Unsupported Transport.", transport)
				response = client.responseUnsupportedTransport(request)
			} else {
				response = client.handleSetup(remote, request)
			}
		case "PLAY":
			response = client.handlePlay(remote, request)
		case "TEARDOWN":
			response = client.handleTeardown(remote, request)
		case "GET_PARAMETER":
			response = client.handleGetParameter(remote, request)
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
		log.Printf("📤 RAW RESPONSE to [%s:%s]:\n%s", client.remoteAddr, client.remotePort, respStr)

		client.ClientConn.Write([]byte(respStr))
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

func (client *Client) handleGetParameter(remote *Remote, request *Request) *Response {
	path := request.GetURL().Path
	session := request.Headers["Session"]
	stream := remote.LookupStream(path)
	response, _ := NewResponse(200, "OK")
	response.Headers["Session"] = session
	response.Headers["Server"] = stream.Server
	return response
}

func (client *Client) handleOptions(remote *Remote, request *Request) *Response {
	streamName := request.GetURL().Path
	stream := remote.LookupStream(streamName)

	// 🔥 SMART PROXY: Если OPTIONS уже закэшированы первым клиентом,
	// отвечаем мгновенно, не отправляя запрос камере и не рискуем оборвать стрим.
	if stream.Options != "" {
		response, _ := NewResponse(200, "OK")
		response.Headers["Public"] = stream.Options
		response.Headers["Server"] = stream.Server
		return response
	}

	// Только для самого первого запроса идем к камере
	URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: streamName}
	optionsRequest, _ := NewRequest("OPTIONS", URL)
	err := remote.SendRequestSync(optionsRequest)
	if err != nil {
		log.Printf("⚠️ Error getting OPTIONS: %v", err)
	}

	response, _ := NewResponse(200, "OK")
	if stream.Options != "" {
		response.Headers["Public"] = stream.Options
	} else {
		response.Headers["Public"] = "OPTIONS, DESCRIBE, SETUP, PLAY, TEARDOWN, GET_PARAMETER"
	}
	response.Headers["Server"] = "RTSP-Proxy/1.0"
	return response
}

func (client *Client) handleSetup(remote *Remote, request *Request) *Response {
	streamName, substreamName := filepath.Split(request.GetURL().Path)
	streamName = filepath.Dir(streamName)
	transport := client.getHeader(request, "Transport")
	ssrc, session, err := remote.GetSsrcSession(client, streamName, substreamName, transport)
	if err != nil {
		log.Printf("Error while setup %s/%s: %v", streamName, substreamName, err)
		remote.Disconnect()
		return client.responseBadRequest(request)
	}
	stream := remote.LookupStream(streamName)
	response, _ := NewResponse(200, "OK")

	proxyIP := client.localAddr
	if proxyIP == "0.0.0.0" {
		proxyIP = "127.0.0.1"
	}

	cleanTransport := regexp.MustCompile(`;?(destination|source)=[^;]+`).ReplaceAllString(transport, "")
	response.Headers["Transport"] = fmt.Sprintf("%s;ssrc=%s;destination=%s;source=%s", cleanTransport, ssrc, client.remoteAddr, proxyIP)
	response.Headers["Cache-Control"] = "must-revalidate"
	response.Headers["Session"] = session + ";timeout=60"
	response.Headers["Server"] = stream.Server
	return response
}

func (client *Client) handleDescribe(remote *Remote, request *Request) *Response {
	path := request.GetURL().Path
	stream := remote.LookupStream(path)

	// 🔥 УМНЫЙ ПРОКСИ: Если SDP уже в кэше, отдаем его сразу, не дергая камеру!
	if stream.SDP != "" {
		response, _ := NewResponse(200, "OK")
		response.Headers["Content-Type"] = "application/sdp"
		response.Headers["Server"] = stream.Server

		proxyIP := client.localAddr
		if proxyIP == "0.0.0.0" || proxyIP == "127.0.0.1" {
			proxyIP = "192.168.65.1"
		}

		rewrittenSDP := strings.ReplaceAll(stream.SDP, "0.0.0.0", proxyIP)
		rewrittenSDP = strings.ReplaceAll(rewrittenSDP, remote.remoteAddr, proxyIP)

		hostIP, _, _ := net.SplitHostPort(remote.Host)
		if hostIP != "" && hostIP != remote.remoteAddr {
			rewrittenSDP = strings.ReplaceAll(rewrittenSDP, hostIP, proxyIP)
		}

		response.Headers["Content-Length"] = strconv.Itoa(len(rewrittenSDP))
		response.Body = rewrittenSDP
		return response
	}

	// Если кэша нет, запрашиваем у камеры
	SDP, err := remote.GetSDP(path)
	if err != nil {
		remote.Disconnect()
		if err.Error() == "unauthorized" {
			return client.responseUnauthorized(request)
		}
		return client.responseBadRequest(request)
	}
	response, _ := NewResponse(200, "OK")
	response.Headers["Content-Type"] = "application/sdp"
	response.Headers["Server"] = stream.Server

	proxyIP := client.localAddr
	if proxyIP == "0.0.0.0" {
		proxyIP = "127.0.0.1"
	}

	// Rewrite SDP to replace remote IP with proxy local IP
	rewrittenSDP := strings.ReplaceAll(SDP, remote.remoteAddr, proxyIP)
	hostIP, _, _ := net.SplitHostPort(remote.Host)
	if hostIP != "" && hostIP != remote.remoteAddr {
		rewrittenSDP = strings.ReplaceAll(rewrittenSDP, hostIP, proxyIP)
	}

	response.Headers["Content-Length"] = strconv.Itoa(len(rewrittenSDP))
	response.Body = rewrittenSDP

	return response
}

func (client *Client) handlePlay(remote *Remote, request *Request) *Response {
	path := filepath.Clean(request.GetURL().Path)
	sessionID := request.Headers["Session"]
	stream := remote.LookupStream(path)

	// 🔥 ВСЕГДА делегируем GetRTPInfo.
	// Внутри него уже есть логика: если Seq == 0, он отправит PLAY камере.
	// Если Seq > 0 (второй клиент), он мгновенно вернет кэшированный RTP-Info, не дергая камеру.
	rtpInfo, err := remote.GetRTPInfo(path, sessionID)
	if err != nil {
		log.Printf("⚠️ Error during PLAY for session %s: %v", sessionID, err)
		remote.Disconnect()
		return client.responseBadRequest(request)
	}

	response, _ := NewResponse(200, "OK")
	response.Headers["Range"] = request.Headers["Range"]
	response.Headers["Session"] = sessionID
	response.Headers["Server"] = stream.Server

	proxyIP := client.localAddr
	if proxyIP == "0.0.0.0" || proxyIP == "127.0.0.1" {
		proxyIP = "192.168.65.1"
	}

	rewrittenRTPInfo := regexp.MustCompile(`url=rtsp://[^/;]+`).ReplaceAllString(rtpInfo, "url=rtsp://"+proxyIP)
	response.Headers["RTP-Info"] = rewrittenRTPInfo

	return response
}

func (client *Client) handleTeardown(remote *Remote, request *Request) *Response {
	path := request.GetURL().Path
	session := request.Headers["Session"]
	stream := remote.LookupStream(path)
	response, _ := NewResponse(200, "OK")
	response.Headers["Session"] = session
	response.Headers["Server"] = stream.Server
	return response
}
