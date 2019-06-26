package rtspproxy

import (
	"fmt"
	"log"
	"net"
	"runtime"
)

type RTSPProxyServer struct {
	rtspPort			int
	rtspListener        *net.TCPListener
	remotes				map[string]*RTSPProxyRemote
}

func NewServer() *RTSPProxyServer {
	runtime.GOMAXPROCS(runtime.NumCPU())

	return &RTSPProxyServer{remotes: make(map[string]*RTSPProxyRemote)}
}

func (server *RTSPProxyServer) Listen (portNum int) error {
	server.rtspPort = portNum

	var err error
	server.rtspListener, err = server.setupOurSocket()

	return err
}

func (server *RTSPProxyServer) setupOurSocket() (*net.TCPListener, error) {
	tcpAddr := fmt.Sprintf("0.0.0.0:%d", server.rtspPort)
	addr, _ := net.ResolveTCPAddr("tcp", tcpAddr)

	return net.ListenTCP("tcp", addr)
}

func (server *RTSPProxyServer) Destroy() {
	server.rtspListener.Close()
}

func (server *RTSPProxyServer) LookupRemote(host string) *RTSPProxyRemote {
	if remote, ok := server.remotes[host]; ok {
		return remote
	}
	remote := NewRemote(server, host)
	server.remotes[host] = remote
	return remote
}

func (server *RTSPProxyServer) Start() {
	go server.incomingConnectionHandler()
}

func (server *RTSPProxyServer) newClientConnection(conn net.Conn) {
	client := NewClient(server, conn)
	if client != nil {
		client.incomingRequestHandler()
	}
}

func (server *RTSPProxyServer) incomingConnectionHandler() {
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
