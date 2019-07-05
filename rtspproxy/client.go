package rtspproxy

import (
	// "errors"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

const rtspBufferSize = 10000

type Client struct {
	ClientConn		net.Conn
	localPort		string
	remotePort		string
	localAddr		string
	remoteAddr		string
	currentCSeq		string
	responseBuffer	string
	host			string
	username		string
	password		string
	server			*Server
}

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
	}

	log.Printf("accepted the client connection [%s:%s].", client.remoteAddr, client.remotePort)

	return client
}

func (client *Client) Destroy() error {
	return client.ClientConn.Close()
}

func (client *Client) incomingRequestHandler() {
	defer func() {
		log.Printf("disconnected the client connection [%s:%s].", client.remoteAddr, client.remotePort)
		client.ClientConn.Close()
		if client.host != "" {
			remote := client.server.LookupRemote(client.host, client.username, client.password)
			if remote != nil {
				// log.Printf("unsubscribing")
				remote.Unsubscribe(client)
			}
		}
	}()

	buffer := make([]byte, rtspBufferSize)
	length := 0

	for {
		recvLen, err := client.ClientConn.Read(buffer[length:])
		if err != nil {
			//logger.Error("conn read data error:", err)
			return
		}

		length += recvLen
		if length == 0 {  // read empty buffer somehow ¯\_(ツ)_/¯
			log.Printf("empty request")
			continue
		}

		if buffer[0] == '$' {
			// TODO: process RTCP packets and send proper response
			// log.Printf("Got RTCP packet: %08x", buffer[:length])
			for length < STREAM_HEADER_LENGTH {
				recvLen, err := client.ClientConn.Read(buffer[length:])
				if err != nil {
					log.Printf("remote conn read data error: %v", err)
					return
				}

				length += recvLen
			}

			// tcpChannel := int(buffer[1])
			streamDataLength := ((int(buffer[2]) << 8) | int(buffer[3]))

			streamDataRecvLength := length - STREAM_HEADER_LENGTH

			for streamDataRecvLength < streamDataLength {
				recvLen, err := client.ClientConn.Read(buffer[length:])
				if err != nil {
					log.Printf("remote conn read data error: %v", err)
					return
				}

				length += recvLen
				streamDataRecvLength = length - STREAM_HEADER_LENGTH
			}

			length = copy(buffer, buffer[STREAM_HEADER_LENGTH+streamDataLength:length])
		} else {
			reqStr := string(buffer[:length])

			length = 0
			request, err := NewRequestFromBuffer(reqStr)

			if err != nil {
				// bad request
				return
			}

			if client.host == "" {
				client.username = request.URL.User.Username()
				client.password, _ = request.URL.User.Password()
				client.host = request.URL.Host
			}
			remote := client.server.LookupRemote(client.host, client.username, client.password)

			if remote == nil {
				response := client.responseNotFound(request)
				client.ClientConn.Write([]byte(response.String()))
				return
			}

			// log.Printf("Got request from client:\n%s", request.String())

			response := client.responseBadRequest(request)
			switch request.Method {
			case "OPTIONS":
				response = client.handleOptions(remote, request)
			case "DESCRIBE":
				response = client.handleDescribe(remote, request)
			case "SETUP":
				response = client.handleSetup(remote, request)
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

			// log.Printf("Sending response to client:\n%s", response)

			client.ClientConn.Write([]byte(response.String()))
		}
	}
}

func (client *Client) getHeader(request *Request, key string) string {
	value := ""

	if _value, ok := request.Headers[key]; ok {
		value = _value
	}

	return value
}

func (client *Client) responseNotFound(request *Request) *Response {
	response, _ := NewResponse(404, "Stream Not Found")
	return response
}

func (client *Client) responseBadRequest(request *Request) *Response {
	response, _ := NewResponse(400, "Bad Request")
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
	path := request.GetURL().Path
	options, err := remote.GetOptions(path)
	if err != nil {
		return client.responseBadRequest(request)
	}
	stream := remote.LookupStream(path)
	response, _ := NewResponse(200, "OK")
	response.Headers["Public"] = options
	response.Headers["Server"] = stream.Server
	return response
}

func (client *Client) handleSetup(remote *Remote, request *Request) *Response {
	streamName, substreamName := filepath.Split(request.GetURL().Path)
	streamName = filepath.Dir(streamName)
	transport := client.getHeader(request, "Transport")
	ssrc, session, err := remote.GetSsrcSession(client, streamName, substreamName, transport)
	if err != nil {
		log.Printf("Error while setup %s/%s: %v", streamName, substreamName, err)
		return client.responseBadRequest(request)
	}
	stream := remote.LookupStream(streamName)
	response, _ := NewResponse(200, "OK")
	response.Headers["Transport"] = fmt.Sprintf("%s;ssrc=%s;destination=%s;source=%s", transport, ssrc,
		client.remoteAddr, client.localAddr)
	response.Headers["Cache-Control"] = "must-revalidate"
	response.Headers["Session"] = session + ";timeout=60"
	response.Headers["Server"] = stream.Server
	return response
}

func (client *Client) handleDescribe(remote *Remote, request *Request) *Response {
	path := request.GetURL().Path
	SDP, err := remote.GetSDP(path)
	if err != nil {
		return client.responseBadRequest(request)
	}
	stream := remote.LookupStream(path)
	response, _ := NewResponse(200, "OK")
	response.Headers["Content-Type"] = "application/sdp"
	response.Headers["Server"] = stream.Server
	response.Headers["Content-Length"] = strconv.Itoa(len(SDP))

	// TODO: rewrite SDP
	response.Body = SDP

	return response
}

func (client *Client) handlePlay(remote *Remote, request *Request) *Response {
	path := filepath.Clean(request.GetURL().Path)
	session := request.Headers["Session"]
	rtpInfo, err := remote.GetRTPInfo(path, session)
	if err != nil {
		return client.responseBadRequest(request)
	}
	stream := remote.LookupStream(path)
	response, _ := NewResponse(200, "OK")
	response.Headers["Range"] = request.Headers["Range"]
	response.Headers["Session"] = session
	response.Headers["Server"] = stream.Server
	// TODO: rewrite rtpInfo
	response.Headers["RTP-Info"] = rtpInfo

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
