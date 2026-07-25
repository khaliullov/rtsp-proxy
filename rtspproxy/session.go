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

// Start begins the session's keep-alive mechanism.
func (session *Session) Start() {
	timeout := session.Timeout - 5
	if timeout < 0 {
		timeout = 1
	}
	if session.started == false {
		session.started = true
		ticker := time.NewTicker(time.Duration(timeout) * time.Second)
		session.quit = make(chan struct{})

		go func() {
			defer func() {
				if r := recover(); r != nil {
					LogCriticalf("⚠️ [SESSION] Keep-alive panic recovered for session %s: %v", session.Session, r)
				}
				ticker.Stop()
				session.started = false
			}()

			for {
				select {
				case <-ticker.C:
					remote := session.Stream.Remote
					if remote == nil {
						LogCriticalf("⚠️ [SESSION] Remote is nil, stopping keep-alive for session %s", session.Session)
						return
					}

					URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: session.Stream.StreamName}
					request, _ := NewRequest("GET_PARAMETER", URL)
					request.Headers["Session"] = session.Session

					err := remote.SendRequestSync(request)
					if err != nil {
						LogCriticalf("⚠️ [SESSION] Keep-alive failed for session %s: %v. Stopping.", session.Session, err)
						return
					}

					subscribers := 0
					for e := session.Transports.Front(); e != nil; e = e.Next() {
						transport := e.Value.(*Transport)
						if interlayer, ok := remote.interlayers[transport.Substreams[0].Channel]; ok {
							subscribers += interlayer.Subscribers.Len()
						}
						if interlayer, ok := remote.interlayers[transport.Substreams[1].Channel]; ok {
							subscribers += interlayer.Subscribers.Len()
						}
					}

					if subscribers == 0 {
						session.nosubscribers++
					} else {
						session.nosubscribers = 0
					}

					if session.nosubscribers > 5 {
						LogCriticalf("No subscribers for a long time for session %s. Tearing down.", session.Session)
						tdRequest, _ := NewRequest("TEARDOWN", URL)
						tdRequest.Headers["Session"] = session.Session
						go remote.SendRequest(tdRequest)
						return
					}

				case <-session.quit:
					return
				}
			}
		}()
	}
}
