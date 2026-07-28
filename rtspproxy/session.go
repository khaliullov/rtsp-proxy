package rtspproxy

import (
	"container/list"
	"net/url"
	"time"
)

// Session represents an RTSP session with a remote server.
type Session struct {
	Stream        *Stream
	Session       string
	Timeout       int
	Transports    *list.List
	started       bool
	quit          chan struct{}
	nosubscribers int
}

// NewSession creates a new Session instance.
func NewSession(stream *Stream, session string, timeout int) *Session {
	return &Session{
		Stream:        stream,
		Session:       session,
		Timeout:       timeout,
		Transports:    list.New(),
		started:       false,
		nosubscribers: 0,
	}
}

// LookupTransport retrieves an existing transport for a substream or creates a new one.
func (session *Session) LookupTransport(substreamName, protocol, comType string) *Transport {
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
	// 🔥 БЕЗОПАСНОЕ ЗАКРЫТИЕ: предотвращаем панику "close of closed channel"
	if session.started {
		session.started = false
		select {
		case <-session.quit:
			// Канал уже закрыт, ничего не делаем
		default:
			close(session.quit)
		}
	}
}

// StartUpstream begins the session's keep-alive mechanism without checking for subscribers.
func (session *Session) StartUpstream() {
	timeout := session.Timeout - 5
	if timeout < 0 {
		timeout = 1
	}
	if session.started == false {
		session.started = true
		ticker := time.NewTicker(time.Duration(timeout) * time.Second)
		session.quit = make(chan struct{})

		go func() {
			defer ticker.Stop()
			defer func() { session.started = false }()

			for {
				select {
				case <-ticker.C:
					// Upstream keep-alive
					if session.Stream == nil || session.Stream.remote == nil {
						return
					}
					remote := session.Stream.remote
					URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: session.Stream.Path}
					request, _ := NewRequest("GET_PARAMETER", URL)
					request.Headers["Session"] = session.Session
					remote.SendRequestSync(request)
				case <-session.quit:
					return
				}
			}
		}()
	}
}
