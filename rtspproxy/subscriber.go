package rtspproxy

type Subscriber struct {
	Client		*Client
	Channel		int
}

func NewSubscriber(client *Client, channel int) *Subscriber {
	subscriber := &Subscriber{
		Client: client,
		Channel: channel,
	}
	return subscriber
}
