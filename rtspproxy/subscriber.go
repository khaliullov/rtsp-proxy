package rtspproxy

// Subscriber represents a client subscribed to an interleaved channel.
type Subscriber struct {
	Client  *Client
	Channel int
}

// NewSubscriber creates a new Subscriber instance.
func NewSubscriber(client *Client, channel int) *Subscriber {
	subscriber := &Subscriber{
		Client:  client,
		Channel: channel,
	}
	return subscriber
}
