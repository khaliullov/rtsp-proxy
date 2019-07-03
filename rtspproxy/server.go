package rtspproxy

import (
	"fmt"
	"log"
	"net"
	"runtime"
)

type Server struct {
	rtspPort			int
	rtspListener        *net.TCPListener
	remotes				map[string]*Remote
}

func NewServer() *Server {
	runtime.GOMAXPROCS(runtime.NumCPU())

	return &Server{remotes: make(map[string]*Remote)}
}

func (server *Server) Listen(portNum int) error {
	server.rtspPort = portNum

	var err error
	server.rtspListener, err = server.setupOurSocket()

	return err
}

func (server *Server) setupOurSocket() (*net.TCPListener, error) {
	tcpAddr := fmt.Sprintf("0.0.0.0:%d", server.rtspPort)
	addr, _ := net.ResolveTCPAddr("tcp", tcpAddr)

	return net.ListenTCP("tcp", addr)
}

func (server *Server) Destroy() {
	server.rtspListener.Close()
}

func (server *Server) LookupRemote(host, username, password string) *Remote {
	if remote, ok := server.remotes[host]; ok {
		return remote
	}
	remote := NewRemote(server, host, username, password)
	server.remotes[host] = remote
	return remote
}

func (server *Server) RemoveRemote(host string) {
	if _, ok := server.remotes[host]; ok {
		delete(server.remotes, host)
	}
}

func (server *Server) Start() {
	go server.incomingConnectionHandler()
}

func (server *Server) newClientConnection(conn net.Conn) {
	client := NewClient(server, conn)
	if client != nil {
		client.incomingRequestHandler()
	}
}

func (server *Server) incomingConnectionHandler() {
	for {
		tcpConn, err := server.rtspListener.AcceptTCP()
		if err != nil {
			log.Printf("failed to accept client. %s", err.Error())
			continue
		}

		tcpConn.SetReadBuffer(50 * 1024)

		// Create a new object for handling server RTSP connection:
		go server.newClientConnection(tcpConn)
	}
}
