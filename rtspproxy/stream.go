package rtspproxy

type Stream struct {
	StreamName		string
	SDP				string
	Options			string
	Server			string
	Remote			*Remote
	// Transports		*list.List  // map[string]*Transport
	Sessions		map[string]*Session
}

func NewStream(remote *Remote, streamName string) *Stream {
	stream := &Stream{
		StreamName: streamName,
		// Transports: list.New(),
		Remote: remote,
		Sessions: make(map[string]*Session),
	}
	return stream
}

func (stream *Stream) LookupTransport(substreamName, protocol, comType string) *Transport {
	for _, session := range stream.Sessions {
		for e := session.Transports.Front(); e != nil; e = e.Next() {
			transport := e.Value.(*Transport)
			if transport.SubstreamName == substreamName && transport.Protocol == protocol && transport.ComType == comType {
				return transport
			}
		}
	}

	return nil
}



func (stream *Stream) LookupSession(sessionId string, args ...int) *Session {
	timeout := 60

	if (len(args) > 0) {
		timeout = args[0]
	}

	if session, ok := stream.Sessions[sessionId]; ok {
		return session
	}

	session := NewSession(stream, sessionId, timeout)
	stream.Sessions[sessionId] = session

	return session
}
