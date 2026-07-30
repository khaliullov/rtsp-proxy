package rtspproxy

import (
	"sync"
	"time"
)

// ClientSession represents a client's active subscription to a stream.
type ClientSession struct {
	client    *Client
	stream    *Stream
	queue     chan []byte
	sessionID string
	quit      chan struct{}
	wg        sync.WaitGroup
	active    bool
	mu        sync.Mutex

	// Channels mapping: upstream channel -> client channel
	channels map[int]int
}

// NewClientSession creates a new ClientSession.
func NewClientSession(client *Client, stream *Stream, sessionID string) *ClientSession {
	return &ClientSession{
		client:    client,
		stream:    stream,
		sessionID: sessionID,
		queue:     make(chan []byte, GlobalConfig.PacketQueueSize), // Buffered queue for fanout
		quit:      make(chan struct{}),
		channels:  make(map[int]int),
	}
}

// Start begins the client session writer.
func (cs *ClientSession) Start() {
	cs.mu.Lock()
	if cs.active {
		cs.mu.Unlock()
		return
	}
	cs.active = true
	cs.mu.Unlock()

	cs.wg.Add(1)
	go cs.run()
}

func (cs *ClientSession) run() {
	defer cs.wg.Done()
	// Ensure queue is drained and buffers returned to pool on exit
	defer func() {
		for {
			select {
			case data := <-cs.queue:
				if len(data) > 0 && data[0] == '$' && cap(data) == GlobalConfig.BufferSize {
					packetPool.Put(data[:cap(data)])
				}
			default:
				return
			}
		}
	}()

	for {
		select {
		case <-cs.quit:
			return
		case <-cs.client.server.ctx.Done():
			return
		case packet, ok := <-cs.queue:
			if !ok {
				return
			}
			// Forward packet to client's main write channel
			select {
			case cs.client.writeChan <- packet:
			case <-time.After(100 * time.Millisecond):
				LogCriticalf("Slow client [%s]: dropping packet and disconnecting", cs.client.remoteAddr)
				// Return current packet to pool since it won't be handled by Client.writer
				if len(packet) > 0 && packet[0] == '$' && cap(packet) == GlobalConfig.BufferSize {
					packetPool.Put(packet[:cap(packet)])
				}
				cs.client.Destroy()
				return
			}
		}
	}
}

// Stop terminates the client session.
func (cs *ClientSession) Stop() {
	cs.mu.Lock()
	if !cs.active {
		cs.mu.Unlock()
		return
	}
	cs.active = false
	close(cs.quit)
	cs.mu.Unlock()
	cs.wg.Wait()
}

// Push adds a packet to the client's queue.
// Returns false if the queue is full (slow client).
func (cs *ClientSession) Push(packet []byte) bool {
	select {
	case cs.queue <- packet:
		return true
	default:
		return false
	}
}

// QueueDepth returns the number of packets currently in the queue.
func (cs *ClientSession) QueueDepth() int {
	return len(cs.queue)
}
