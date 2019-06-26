package rtspproxy

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"regexp"
	"strings"
)

const rtspBufferSize = 10000

type RTSPProxyClient struct {
	clientConn		net.Conn
	localPort		string
	remotePort		string
	localAddr		string
	remoteAddr		string
	currentCSeq		string
	responseBuffer	string
	server			*RTSPProxyServer
	digest			*Digest
}

func NewClient(server *RTSPProxyServer, socket net.Conn) *RTSPProxyClient {
	localAddr := strings.Split(socket.LocalAddr().String(), ":")
	remoteAddr := strings.Split(socket.RemoteAddr().String(), ":")
	return &RTSPProxyClient{
		server:     server,
		clientConn:	socket,
		localAddr:  localAddr[0],
		localPort:  localAddr[1],
		remoteAddr: remoteAddr[0],
		remotePort: remoteAddr[1],
		digest:		NewDigest(),
	}
}

func (client *RTSPProxyClient) destroy() error {
	return client.clientConn.Close()
}

func (client *RTSPProxyClient) incomingRequestHandler() {
	defer client.clientConn.Close()

	var isclose bool
	buffer := make([]byte, rtspBufferSize)
	for {
		length, err := client.clientConn.Read(buffer)

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

	log.Printf("disconnected the connection[%s:%s].", client.remoteAddr, client.remotePort)
	/* if c.clientSession != nil {
		c.clientSession.destroy()
	} */
}

func (client *RTSPProxyClient) handleRequestBytes(buffer []byte, length int) error {
	if length < 0 {
		return errors.New("EOF")
	}

	reqStr := string(buffer[:length])

	// request, err := client.parseRequest(reqStr)
	request, err := NewRequest(reqStr)
	cseq := ""

	if _cseq, ok := request.Headers["CSeq"]; ok {
		cseq = _cseq
	}

	if err != nil {
		// bad request
	}

	remote := client.server.LookupRemote(request.URL.Host)

	client.createAuthenticatorStr(request)

	log.Printf("Got request: %s", request.String())

	res, responseLength, err := remote.Request(request)

	response, err := NewResponse(string(res[:responseLength]))

	if err != nil {
		return err
	}

	if cseq != "" {
		response.Headers["CSeq"] = cseq
	}
	if _, ok := response.Headers["Content-Base"]; ok {
		response.Headers["Content-Base"] = request.RawURL + "/"
	}

	if response.Code == 401 {
		if wwwAuthenticate, ok := response.Headers["WWW-Authenticate"]; ok {
			if request.URL.User != nil {
				client.digest.Username = request.URL.User.Username()
				client.digest.Password, _ = request.URL.User.Password()
			}
			if client.handleAuthenticationFailure(wwwAuthenticate) {
				return client.handleRequestBytes(buffer, length)
			}
		}
	}

	log.Printf("Got response: %s", response)

	client.clientConn.Write([]byte(response.String()))

	log.Printf("Received %d new bytes of request data.", length)
	return nil
}

func (client *RTSPProxyClient) handleAuthenticationFailure(paramsStr string) bool {
	// There was no "WWW-Authenticate:" header; we can't proceed.
	if paramsStr == "" {
		return false
	}

	digest_regex := regexp.MustCompile(`Digest realm="([^"]+)", nonce="([^"]+)"`)
	basic_regex := regexp.MustCompile(`Basic realm="([^"]+)"`)

	// Fill in "fCurrentAuthenticator" with the information from the "WWW-Authenticate:" header:
	var matches []string
	success := true
	alreadyHadRealm := client.digest.Realm != ""

	if matches = digest_regex.FindStringSubmatch(paramsStr); len(matches) == 3 {
		client.digest.Realm = matches[1]
		client.digest.Nonce = matches[2]
	} else if matches = basic_regex.FindStringSubmatch(paramsStr); len(matches) == 2 {
		client.digest.Realm = matches[1]
		client.digest.RandomNonce()
	} else {
		success = false // bad "WWW-Authenticate:" header
	}

	// We already had a 'realm', or don't have a username and/or password,
	// so the new "WWW-Authenticate:" header information won't help us.  We remain unauthenticated.
	if alreadyHadRealm || client.digest.Username == "" || client.digest.Password == "" {
		success = false
	}

	return success
}

func (client *RTSPProxyClient) createAuthenticatorStr(request *Request) {
	if client.digest.Realm != "" && client.digest.Username != "" && client.digest.Password != "" {
		var response string
		if client.digest.Nonce != "" { // digest authentication
			URL := request.GetURL()
			response = client.digest.ComputeResponse(request.Command, URL)
			request.Headers["Authorization"] = fmt.Sprintf("Digest username=\"%s\", "+
				"realm=\"%s\", nonce=\"%s\", uri=\"%s\", response=\"%s\"",
				client.digest.Username, client.digest.Realm, client.digest.Nonce, URL, response)
		} else { // basic authentication
			usernamePassword := fmt.Sprintf("%s:%s", client.digest.Username, client.digest.Password)
			response = base64.StdEncoding.EncodeToString([]byte(usernamePassword))
			request.Headers["Authorization"] = fmt.Sprintf("Basic %s", response)
		}
	}
}
