package rtspproxy

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Server represents the RTSP proxy server.
type Server struct {
	ctx           context.Context
	cancel        context.CancelFunc
	rtspPort      int
	rtspListener  *net.TCPListener
	streamManager *StreamManager
	clients       sync.WaitGroup // To track active client connections
}

// NewServer creates a new Server instance.
func NewServer(ctx context.Context) *Server {
	runtime.GOMAXPROCS(runtime.NumCPU())

	serverCtx, cancel := context.WithCancel(ctx)
	s := &Server{
		ctx:    serverCtx,
		cancel: cancel,
	}
	s.streamManager = NewStreamManager(s)
	return s
}

// Listen starts the server listening on the specified port.
func (server *Server) Listen(portNum int) error {
	server.rtspPort = portNum

	var err error
	server.rtspListener, err = server.setupOurSocket()
	if err != nil {
		return fmt.Errorf("failed to setup socket: %w", err)
	}
	return nil
}

func (server *Server) setupOurSocket() (*net.TCPListener, error) {
	tcpAddr := fmt.Sprintf("0.0.0.0:%d", server.rtspPort)
	addr, err := net.ResolveTCPAddr("tcp", tcpAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve TCP address: %w", err)
	}

	listener, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on TCP: %w", err)
	}
	return listener, nil
}

// Shutdown gracefully shuts down the server.
func (server *Server) Shutdown(ctx context.Context) error {
	LogCriticalf("Initiating server shutdown...")

	// 1. Stop accepting new connections
	if server.rtspListener != nil {
		if err := server.rtspListener.Close(); err != nil {
			LogCriticalf("Error closing RTSP listener: %v", err)
		}
	}

	// 2. Signal all goroutines to stop
	server.cancel()

	// 3. Wait for all clients to finish
	done := make(chan struct{})
	go func() {
		server.clients.Wait()
		close(done)
	}()

	select {
	case <-done:
		LogCriticalf("All client connections closed.")
	case <-ctx.Done():
		LogCriticalf("Shutdown context timed out while waiting for clients to close.")
		return ctx.Err()
	}

	// 4. Shutdown stream manager
	server.streamManager.Shutdown()

	LogCriticalf("Server shutdown complete.")
	return nil
}

// LookupStream retrieves an existing stream or creates a new one.
func (server *Server) LookupStream(host, username, password, path string) *Stream {
	return server.streamManager.GetStream(host, username, password, path)
}

// Start begins accepting incoming client connections.
func (server *Server) Start() {
	server.incomingConnectionHandler()
}

func (server *Server) newClientConnection(conn net.Conn) {
	server.clients.Add(1)
	go func() {
		defer server.clients.Done()
		client := NewClient(server, conn)
		if client != nil {
			client.incomingRequestHandler()
		}
	}()
}

func (server *Server) incomingConnectionHandler() {
	for {
		select {
		case <-server.ctx.Done():
			LogCriticalf("Stopping incoming connection handler due to shutdown signal.")
			return
		default:
			if server.rtspListener != nil {
				server.rtspListener.SetDeadline(time.Now().Add(time.Second))
			}
			tcpConn, err := server.rtspListener.AcceptTCP()
			if err != nil {
				// 🔥 ИСПРАВЛЕНИЕ: Тихий выход при закрытии слушателя
				if server.ctx.Err() != nil || strings.Contains(err.Error(), "closed network connection") {
					return
				}
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue // Timeout, check context again
				}
				LogCriticalf("Failed to accept client: %s", err.Error())
				continue
			}

			tcpConn.SetReadBuffer(50 * 1024)
			server.newClientConnection(tcpConn)
		}
	}
}
