package rtspproxy

import (
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
)

type RTSPProxyRemote struct {
	Host			string
	RemoteConn		*net.TCPConn
	localPort		string
	remotePort		string
	localAddr		string
	remoteAddr		string
	currentCSeq		int
	Server 			*RTSPProxyServer
	connMutex    	sync.Mutex
	addr			*net.TCPAddr
	attempts		int
}

func NewRemote(server *RTSPProxyServer, host string) *RTSPProxyRemote {
	addr, err := net.ResolveTCPAddr("tcp", host)

	if err != nil {
		log.Printf("Failed to resolve TCP address.%s\n", err.Error())
		return nil
	}

	remote := &RTSPProxyRemote{
		Host: host,
		Server:		server,
		addr:		addr,
		attempts:	0,
	}
	err = remote.Dial()
	if err != nil {
		return nil
	}

	return remote
}

func (remote *RTSPProxyRemote) Dial() error {
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
	return nil
}

func (remote *RTSPProxyRemote) Destroy() error {
	return remote.RemoteConn.Close()
}

func (remote *RTSPProxyRemote) Request(request *Request) ([]byte, int, error) {
	remote.connMutex.Lock()
	defer remote.connMutex.Unlock()

	remote.currentCSeq++
	request.Headers["CSeq"] = strconv.Itoa(remote.currentCSeq)
	log.Printf("Sending request: %s", request)

	rawRequest := []byte(request.String())
	remote.RemoteConn.Write(rawRequest)

	buffer := make([]byte, rtspBufferSize)
	length, err := remote.RemoteConn.Read(buffer)

	if err != nil {
		remote.attempts++
		if io.EOF == err && remote.attempts <= 1 {
			log.Printf("Remote has hang up. Reconnecting...")
			err = remote.Dial()
			if err == nil {
				return remote.Request(request)
			}
		}
		log.Printf("Failed to handle remote Request Bytes: %v", err)
		return nil, 0, err
	}
	remote.attempts = 0
	return buffer, length, nil
}
