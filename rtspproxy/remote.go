package rtspproxy

import (
	"container/list"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

type Remote struct {
	Host			string
	RemoteConn		*net.TCPConn
	localPort		string
	remotePort		string
	localAddr		string
	remoteAddr		string
	currentCSeq		int
	Server 			*Server
	connMutex    	sync.Mutex
	addr			*net.TCPAddr
	streams			map[string]*Stream
	interlayers     map[int]*Interlayer
	requests		*list.List
	digest			*Digest
}

var (
	STREAM_HEADER_LENGTH = 4
)

func (remote *Remote) LookupStream(streamName string) *Stream {
	if stream, ok := remote.streams[streamName]; ok {
		return stream
	} else {
		stream := NewStream(streamName)
		remote.streams[streamName] = stream
		return stream
	}
}

func NewRemote(server *Server, host, username, password string) *Remote {
	addr, err := net.ResolveTCPAddr("tcp", host)

	if err != nil {
		log.Printf("Failed to resolve TCP address.%s\n", err.Error())
		return nil
	}

	remote := &Remote{
		Host: 			host,
		Server:			server,
		addr:			addr,
		streams:		make(map[string]*Stream),
		interlayers:	make(map[int]*Interlayer),
		requests:		list.New(),
		digest:     	NewDigest(),
	}
	if username != "" {
		remote.digest.Username = username
		remote.digest.Password = password
	}
	err = remote.Dial()
	if err != nil {
		return nil
	}

	return remote
}

func (remote *Remote) Dial() error {
	socket, err := net.DialTCP("tcp", nil, remote.addr)
	if err != nil {
		log.Printf("Failed to connect to the remote server: %s\n", err.Error())
		return err
	}

	localAddr := strings.Split(socket.LocalAddr().String(), ":")
	remoteAddr := strings.Split(socket.RemoteAddr().String(), ":")
	if remote.RemoteConn != nil {
		remote.RemoteConn.Close()
	}
	remote.RemoteConn = socket
	remote.localAddr = localAddr[0]
	remote.localPort = localAddr[1]
	remote.remoteAddr = remoteAddr[0]
	remote.remotePort = remoteAddr[1]
	remote.currentCSeq = 0
	go remote.incomingRequestHandler()
	return nil
}

func (remote *Remote) Destroy() error {
	return remote.RemoteConn.Close()
}

func (remote *Remote) handleStream(tcpChannel, length int, dataBuffer []byte) {
	// log.Printf("Got packet %d", tcpChannel)
	interlayer := remote.interlayers[tcpChannel]
	for e := interlayer.Subscribers.Front(); e != nil; e = e.Next() {
		// log.Printf("Subscriber %d", tcpChannel)
		subscriber := e.Value.(*Subscriber)
		header := make([]byte, 4)
		header[0] = '$'
		header[1] = byte(subscriber.Channel)
		header[2] = byte((length & 0xFF00) >> 8)
		header[3] = byte(length & 0xFF)
		subscriber.Client.ClientConn.Write(header)
		subscriber.Client.ClientConn.Write(dataBuffer)
	}
}

func (remote *Remote) incomingRequestHandler() {
	defer func() {
		if re := recover(); re != nil {
			log.Printf("Remote Handle panic: %v", re)
		}
		remote.RemoteConn.Close()
	}()

	buffer := make([]byte, rtspBufferSize)
	length := 0

	for {
		recvLen, err := remote.RemoteConn.Read(buffer[length:])
		if err != nil {
			//logger.Error("conn read data error:", err)
			return
		}

		length += recvLen

		if buffer[0] == '$' {
			for length < STREAM_HEADER_LENGTH {
				recvLen, err := remote.RemoteConn.Read(buffer[length:])
				if err != nil {
					log.Printf("remote conn read data error: %v", err)
					return
				}

				length += recvLen
			}

			tcpChannel := int(buffer[1])
			streamDataLength := ((int(buffer[2]) << 8) | int(buffer[3]))

			streamDataRecvLength := length - STREAM_HEADER_LENGTH

			for streamDataRecvLength < streamDataLength {
				recvLen, err := remote.RemoteConn.Read(buffer[length:])
				if err != nil {
					log.Printf("remote conn read data error: %v", err)
					return
				}

				length += recvLen
				streamDataRecvLength = length - STREAM_HEADER_LENGTH
			}

			dataBuffer := make([]byte, streamDataLength)
			copy(dataBuffer, buffer[STREAM_HEADER_LENGTH:STREAM_HEADER_LENGTH+streamDataLength])
			length = copy(buffer, buffer[STREAM_HEADER_LENGTH+streamDataLength:length])
			remote.handleStream(tcpChannel, streamDataLength, dataBuffer)
		} else {
			recv := string(buffer[:length])

			response, err := NewResponseFromBuffer(recv)
			if err != nil {
				log.Printf("remote rtsp read request error: %v", err)
				return
			}
			log.Printf("Get response from remote:\n%s", response.String())
			requestEl := remote.requests.Front()
			request := requestEl.Value.(*Request)
			remote.requests.Remove(requestEl)

			if response.Code == 401 && request.Attempts == 0 {
				if wwwAuthenticate, ok := response.Headers["WWW-Authenticate"]; ok {
					if remote.digest.Username != "" && remote.handleAuthenticationFailure(wwwAuthenticate) {
						request.Attempts++
						remote.SendRequest(request)
						length = 0
						continue
					}
				}
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
				}
			}
			for e := request.Subscriptions.Front(); e != nil; e = e.Next() {
				log.Printf("sending OK to chan")
				e.Value.(chan string) <- "ok"
				request.Subscriptions.Remove(e)
			}

			length = 0
		}
	}

	log.Printf("disconnected the connection [%s:%s].", remote.remoteAddr, remote.remotePort)
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
	comType := transportParts[1]
	transportParts = transportParts[2:]
	params := make(map[string]string)
	for _, element := range transportParts {
		kv := strings.Split(element, "=")
		params[kv[0]] = kv[1]
	}
	return protocol, comType, params
}

func (remote *Remote) handleSetup(request *Request, response *Response) {
	streamName, substreamName := filepath.Split(request.URL.Path)
	streamName = filepath.Dir(streamName)
	protocol, comType, params := remote.parseTransport(response.Headers["Transport"])
	stream := remote.LookupStream(streamName)
	transport := stream.LookupTransport(substreamName, protocol, comType)
	transport.Ssrc = params["ssrc"]
	if transport.Session == "" {
		session := strings.Split(response.Headers["Session"], ";")
		transport.Session = session[0]
	}
}

func (remote *Remote) handlePlay(request *Request, response *Response) {
	streamName := request.URL.Path
	stream := remote.LookupStream(streamName)
	rtpInfo := response.Headers["RTP-Info"]
	session := request.Headers["Session"]
	transports := stream.LookupTransportBySession(session)
	// url=rtsp://192.168.20.2/profile1/track1;seq=52326;rtptime=1781120107,url=rtsp://192.168.20.2/profile1/track2;seq=44529;rtptime=572932177
	for _, rtp := range strings.Split(rtpInfo, ",") {
		params := make(map[string]string)
		for _, param := range strings.Split(rtp, ";") {
			kv := strings.Split(param, "=")
			params[kv[0]] = kv[1]
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
}

func (remote *Remote) GetOptions(streamName string) (string, error) {
	stream := remote.LookupStream(streamName)

	if stream.Options == "" {
		URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: streamName}
		request, _ := NewRequest("OPTIONS", URL)
		err := remote.SendRequestSync(request)
		if err != nil {
			return "", err
		}
	}
	return stream.Options, nil
}

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

func (remote *Remote) GetSsrcSession(client *Client, streamName, substreamName, transportStr string) (string, string, error) {
	protocol, comType, params := remote.parseTransport(transportStr)
	stream := remote.LookupStream(streamName)
	transport := stream.LookupTransport(substreamName, protocol, comType)
	if len(transport.Substreams) != 2 {
		if transport.Protocol == "RTP/AVP/TCP" {
			index := len(remote.interlayers)
			URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: streamName + "/" + substreamName}
			request, _ := NewRequest("SETUP", URL)
			request.Headers["Transport"] = fmt.Sprintf("%s;%s;interleaved=%d-%d", transport.Protocol, transport.ComType,
				index, index+1)
			err := remote.SendRequestSync(request)
			if err != nil {
				return "", "", err
			}
			transport.Substreams[0] = NewSubstream(transport, substreamName)
			transport.Substreams[0].Channel = index
			transport.Substreams[1] = NewSubstream(transport, substreamName)
			transport.Substreams[1].Channel = index+1
			remote.interlayers[index] = NewInterlayer(index, stream, transport, transport.Substreams[0])
			channel, _ := strconv.Atoi(strings.Split(params["interleaved"], "-")[0])
			remote.interlayers[index].Subscribers.PushBack(NewSubscriber(client, channel))
			remote.interlayers[index+1] = NewInterlayer(index, stream, transport, transport.Substreams[1])
			remote.interlayers[index+1].Subscribers.PushBack(NewSubscriber(client, channel+1))
		} else {
			return "", "", errors.New("protocol is not supported")
		}
	}
	return transport.Ssrc, transport.Session, nil
}

func (remote *Remote) GetRTPInfo(streamName, session string) (string, error) {
	stream := remote.LookupStream(streamName)
	transports := stream.LookupTransportBySession(session)

	if transports.Len() > 0 {
		transport := transports.Front().Value.(*Transport)
		if (transport.Substreams[0].Channel >= 0 && transport.Substreams[0].Seq == 0) {
			URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: streamName}
			request, _ := NewRequest("PLAY", URL)
			request.Headers["Session"] = session
			request.Headers["Range"] = "npt=0.000-"
			err := remote.SendRequestSync(request)
			if err != nil {
				return "", err
			}
		}
		parts := make([]string, stream.Transports.Len())
		i := 0
		for e := stream.Transports.Front(); e != nil; e = e.Next() {
			transport := e.Value.(*Transport)
			URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: streamName + "/" + transport.SubstreamName}
			parts[i] = fmt.Sprintf("url=%s;seq=%d;rtptime=%d", URL.String(), transport.Substreams[0].Seq,
				transport.Substreams[0].RTPTime)
			i++
		}
		return strings.Join(parts, ","), nil
	}

	return "", errors.New("No streams were setup")
}

func (remote *Remote) SendRequestSync(request *Request) error {
	timeout := 10
	pollInterval := 100
	ipc := NewIPC(timeout, pollInterval)
	request.Subscriptions.PushBack(ipc.Channel)
	remote.SendRequest(request)
	result := ipc.GetResponse()
	if result != "ok" {
		return errors.New("IPC error: " + result)
	}
	return nil
}

func (remote *Remote) SendRequest(request *Request) error {
	remote.currentCSeq++
	request.Headers["CSeq"] = strconv.Itoa(remote.currentCSeq)
	remote.createAuthenticatorStr(request)

	log.Printf("Sending request to remote:\n%s", request)

	remote.requests.PushBack(request)

	rawRequest := []byte(request.String())

	remote.RemoteConn.Write(rawRequest)

	return nil
}

func (remote *Remote) handleAuthenticationFailure(paramsStr string) bool {
	// There was no "WWW-Authenticate:" header; we can't proceed.
	if paramsStr == "" {
		return false
	}

	digestRegex := regexp.MustCompile(`Digest realm="([^"]+)", nonce="([^"]+)"`)
	basicRegex := regexp.MustCompile(`Basic realm="([^"]+)"`)

	// Fill in "fCurrentAuthenticator" with the information from the "WWW-Authenticate:" header:
	var matches []string
	success := true
	alreadyHadRealm := remote.digest.Realm != ""

	if matches = digestRegex.FindStringSubmatch(paramsStr); len(matches) == 3 {
		remote.digest.Realm = matches[1]
		remote.digest.Nonce = matches[2]
	} else if matches = basicRegex.FindStringSubmatch(paramsStr); len(matches) == 2 {
		remote.digest.Realm = matches[1]
		remote.digest.RandomNonce()
	} else {
		success = false // bad "WWW-Authenticate:" header
	}

	// We already had a 'realm', or don't have a username and/or password,
	// so the new "WWW-Authenticate:" header information won't help us.  We remain unauthenticated.
	if alreadyHadRealm || remote.digest.Username == "" || remote.digest.Password == "" {
		success = false
	}

	return success
}

func (remote *Remote) createAuthenticatorStr(request *Request) {
	if remote.digest.Realm != "" && remote.digest.Username != "" && remote.digest.Password != "" {
		var response string
		if remote.digest.Nonce != "" { // digest authentication
			URL := request.GetURL().String()
			response = remote.digest.ComputeResponse(request.Method, URL)
			request.Headers["Authorization"] = fmt.Sprintf("Digest username=\"%s\", "+
				"realm=\"%s\", nonce=\"%s\", uri=\"%s\", response=\"%s\"",
				remote.digest.Username, remote.digest.Realm, remote.digest.Nonce, URL, response)
		} else { // basic authentication
			usernamePassword := fmt.Sprintf("%s:%s", remote.digest.Username, remote.digest.Password)
			response = base64.StdEncoding.EncodeToString([]byte(usernamePassword))
			request.Headers["Authorization"] = fmt.Sprintf("Basic %s", response)
		}
	}
}
