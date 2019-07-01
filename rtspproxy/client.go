package rtspproxy

import (
	"errors"
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
	return &Client{
		server:     server,
		ClientConn: socket,
		localAddr:  localAddr[0],
		localPort:  localAddr[1],
		remoteAddr: remoteAddr[0],
		remotePort: remoteAddr[1],
	}
}

func (client *Client) destroy() error {
	return client.ClientConn.Close()
}

func (client *Client) incomingRequestHandler() {
	defer client.ClientConn.Close()

	var isclose bool
	buffer := make([]byte, rtspBufferSize)
	for {
		length, err := client.ClientConn.Read(buffer)

		switch err {
		case nil:
			err = client.handleRequestBytes(buffer, length)
			if err != nil {
				log.Printf("Failed to handle client Request Bytes: %v", err)
				isclose = true
			}
		default:
			log.Printf("default: %v", err)
			if err.Error() == "EOF" {
				isclose = true
			}
		}

		if isclose {
			break
		}
	}

	remote := client.server.LookupRemote(client.host, client.username, client.password)
	if remote != nil {
		remote.Unsubscribe(client)
	}
	log.Printf("disconnected the connection[%s:%s].", client.remoteAddr, client.remotePort)
	/* if c.clientSession != nil {
		c.clientSession.destroy()
	} */
}

func (client *Client) handleRequestBytes(buffer []byte, length int) error {
	if length < 0 {
		return errors.New("EOF")
	}

	reqStr := string(buffer[:length])

	// request, err := client.parseRequest(reqStr)
	request, err := NewRequestFromBuffer(reqStr)

	if err != nil {
		// bad request
		return nil
	}

	client.username = request.URL.User.Username()
	client.password, _ = request.URL.User.Password()
	client.host = request.URL.Host
	remote := client.server.LookupRemote(client.host, client.username, client.password)

	log.Printf("Got request from client:\n%s", request.String())

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
	}

	response.Headers["Via"] = "RTSP-Proxy"
	cseq := client.getHeader(request, "CSeq")
	if cseq != "" {
		response.Headers["CSeq"] = cseq
	}

	log.Printf("Sending response to client:\n%s", response)

	client.ClientConn.Write([]byte(response.String()))

	log.Printf("Received %d new bytes of request data.", length)
	return nil
}

func (client *Client) getHeader(request *Request, key string) string {
	value := ""

	if _value, ok := request.Headers[key]; ok {
		value = _value
	}

	return value
}

func (client *Client) responseBadRequest(request *Request) *Response {
	response, _ := NewResponse(400, "Bad Request")
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
	path := request.GetURL().Path
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
