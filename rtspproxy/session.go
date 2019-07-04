package rtspproxy

import (
	"container/list"
	"log"
	"net/url"
	"time"
)

type Session struct {
	Stream			*Stream
	Session			string
	Timeout			int
	Transports		*list.List
	started			bool
	quit			chan struct{}
	nosubscribers	int
}

func NewSession(stream *Stream, session string, timeout int) *Session {
	return &Session{
		Stream: stream,
		Session: session,
		Timeout: timeout,
		Transports: list.New(),
		started: false,
		nosubscribers: 0,
	}
}

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

func (session *Session) Stop() {
	if session.started {
		close(session.quit)
		session.started = false
	}
}

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
			for {
				select {
				case <- ticker.C:
					remote := session.Stream.Remote
					URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: session.Stream.StreamName}
					request, _ := NewRequest("GET_PARAMETER", URL)
					request.Headers["Session"] = session.Session
					remote.SendRequestSync(request)
					subscribers := 0
					for e := session.Transports.Front(); e != nil; e = e.Next() {
						transport := e.Value.(*Transport)
						subscribers += remote.interlayers[transport.Substreams[0].Channel].Subscribers.Len()
						subscribers += remote.interlayers[transport.Substreams[1].Channel].Subscribers.Len()
					}
					if subscribers == 0 {
						session.nosubscribers++
					} else {
						session.nosubscribers = 0
					}
					if (session.nosubscribers > 5) {
						log.Printf("not subscribers for a long time")
						request, _ := NewRequest("TEARDOWN", URL)
						request.Headers["Session"] = session.Session
						remote.SendRequest(request)
						ticker.Stop()
						session.started = false
					}
				case <- session.quit:
					ticker.Stop()
					session.started = false
					return
				}
			}
		}()
	}
}
