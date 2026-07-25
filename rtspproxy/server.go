package rtspproxy

import (
	"fmt"
	"net"
	"runtime"
)

// Server represents the RTSP proxy server.
type Server struct {
	rtspPort     int
	rtspListener *net.TCPListener
	remotes      map[string]*Remote
}

// NewServer creates a new Server instance.
func NewServer() *Server {
	runtime.GOMAXPROCS(runtime.NumCPU())

	return &Server{remotes: make(map[string]*Remote)}
}

// Listen starts the server listening on the specified port.
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

// Destroy closes the server's listener.
func (server *Server) Destroy() {
	server.rtspListener.Close()
}

// LookupRemote retrieves an existing remote connection or creates a new one.
func (server *Server) LookupRemote(host, username, password string) *Remote {
	if remote, ok := server.remotes[host]; ok {
		return remote
	}
	remote := NewRemote(server, host, username, password)
	if remote == nil {
		LogCriticalf("Failed to connect to remote host: %s", host)
		return nil
	}
	server.remotes[host] = remote
	return remote
}

// RemoveRemote removes a remote connection from the server's management.
func (server *Server) RemoveRemote(host string) {
	if _, ok := server.remotes[host]; ok {
		delete(server.remotes, host)
	}
}

// Start begins accepting incoming client connections.
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
			LogCriticalf("failed to accept client. %s", err.Error())
			continue
		}

		tcpConn.SetReadBuffer(50 * 1024)

		// Create a new object for handling server RTSP connection:
		go server.newClientConnection(tcpConn)
	}
}
