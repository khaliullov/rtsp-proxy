package rtspproxy

// Stream represents an RTSP stream from a remote server.
type Stream struct {
	StreamName string
	SDP        string
	Options    string
	Server     string
	Remote     *Remote
	Sessions   map[string]*Session
}

// NewStream creates a new Stream instance.
func NewStream(remote *Remote, streamName string) *Stream {
	stream := &Stream{
		StreamName: streamName,
		Remote:     remote,
		Sessions:   make(map[string]*Session),
	}
	return stream
}

// LookupTransport retrieves an existing transport for a substream within any session of this stream.
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

// LookupSession retrieves an existing session or creates a new one for the stream.
func (stream *Stream) LookupSession(sessionID string, args ...int) *Session {
	timeout := 60

	if len(args) > 0 {
		timeout = args[0]
	}

	if session, ok := stream.Sessions[sessionID]; ok {
		return session
	}

	session := NewSession(stream, sessionID, timeout)
	stream.Sessions[sessionID] = session

	return session
}
