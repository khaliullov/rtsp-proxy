package rtspproxy

import (
	"container/list"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// Session represents an RTSP session with a remote server.
type Session struct {
	Stream     *Stream
	Session    string
	Timeout    int
	Transports *list.List
	started    atomic.Bool
	quit       chan struct{}
	mu         sync.RWMutex
}

// NewSession creates a new Session instance.
func NewSession(stream *Stream, session string, timeout int) *Session {
	return &Session{
		Stream:     stream,
		Session:    session,
		Timeout:    timeout,
		Transports: list.New(),
	}
}

// LookupTransport retrieves an existing transport for a substream or creates a new one.
func (session *Session) LookupTransport(substreamName, protocol, comType string) *Transport {
	session.mu.Lock()
	defer session.mu.Unlock()

	for e := session.Transports.Front(); e != nil; e = e.Next() {
		transport := e.Value.(*Transport)
		if transport.SubstreamName == substreamName && transport.Protocol == protocol && transport.ComType == comType {
			return transport
		}
	}

	transport := NewTransport(session, substreamName, protocol, comType)
	session.Transports.PushBack(transport)

	return transport
}

// Stop terminates the session's keep-alive mechanism.
func (session *Session) Stop() {
	session.mu.Lock()
	defer session.mu.Unlock()

	if session.started.Swap(false) {
		if session.quit != nil {
			close(session.quit)
			session.quit = nil
		}
	}
}

// StartUpstream begins the session's keep-alive mechanism without checking for subscribers.
func (session *Session) StartUpstream() {
	session.mu.Lock()
	if session.started.Swap(true) {
		session.mu.Unlock()
		return
	}
	session.quit = make(chan struct{})
	quit := session.quit

	s := session.Stream
	if s == nil {
		session.started.Store(false)
		session.mu.Unlock()
		return
	}
	ctx := s.ctx // Use stream context for immediate cancellation
	session.mu.Unlock()

	timeout := session.Timeout - 5
	if timeout < 0 {
		timeout = 1
	}

	ticker := time.NewTicker(time.Duration(timeout) * time.Second)

	go func() {
		defer ticker.Stop()
		defer session.started.Store(false)

		for {
			select {
			case <-ticker.C:
				s.mu.RLock()
				remote := s.remote
				s.mu.RUnlock()

				if remote == nil {
					return
				}

				URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: s.Path}
				request, _ := NewRequest("GET_PARAMETER", URL)
				request.Headers["Session"] = session.Session

				// Send synchronously but respect context
				_ = remote.SendRequestSync(request)
			case <-quit:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}
