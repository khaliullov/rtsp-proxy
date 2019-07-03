package rtspproxy

import (
	"container/list"
	"net/url"
	"time"
)

type Session struct {
	Stream			*Stream
	Session			string
	Timeout			int
	Transports		*list.List
	started			bool
}

func NewSession(stream *Stream, session string, timeout int) *Session {
	return &Session{
		Stream: stream,
		Session: session,
		Timeout: timeout,
		Transports: list.New(),
		started: false,
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

func (session *Session) Start() {
	// timeout := session.Timeout - 5
	timeout := 5
	if timeout < 0 {
		timeout = 1
	}
	if session.started == false {
		session.started = true
		ticker := time.NewTicker(time.Duration(timeout) * time.Second)
		quit := make(chan struct{})

		go func() {
			for {
				select {
				case <- ticker.C:
					remote := session.Stream.Remote
					URL := &url.URL{Scheme: "rtsp", Host: remote.Host, Path: session.Stream.StreamName}
					request, _ := NewRequest("GET_PARAMETER", URL)
					request.Headers["Session"] = session.Session
					remote.SendRequest(request)
					// TODO: check subscriptions
					// If absend - destroy self
				case <- quit:
					ticker.Stop()
					session.started = false
					return
				}
			}
		}()
	}
}
