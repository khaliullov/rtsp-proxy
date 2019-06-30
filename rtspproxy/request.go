package rtspproxy

import (
	"container/list"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strings"
)

type Request struct {
	Method          string
	RawURL          string
	URL             *url.URL
	ProtocolVersion string
	Headers         map[string]string
	Body            []byte
	Attempts        int
	Subscriptions   *list.List
}

func NewRequest(method string, URL *url.URL, args ...string) (*Request, error) {
	protocolVersion := "RTSP/1.0"
	if len(args) > 0 && args[0] != "" {
		protocolVersion = args[0]
	}
	request := &Request{
		Method:          method,
		ProtocolVersion: protocolVersion,
		Headers:         make(map[string]string),
		URL:             URL,
		Subscriptions:   list.New(),
	}
	return request, nil
}

func NewRequestFromBuffer(buffer string) (*Request, error) {
	request := &Request{
		Headers:         make(map[string]string),
		Subscriptions:   list.New(),
	}
	if buffer != "" {
		err := request.ParseRequest(buffer)
		if err != nil {
			return nil, err
		}
	}
	return request, nil
}

func (request *Request) getLine(startOfLine string) (thisLineStart, nextLineStart string) {
	var index int
	for i, c := range startOfLine {
		// Check for the end of line: \r\n (but also accept \r or \n by itself):
		if c == '\r' || c == '\n' {
			if c == '\r' {
				if startOfLine[i+1] == '\n' {
					index = i + 2 // skip "\r\n"
				}
			} else {
				index = i + 1
			}

			thisLineStart = startOfLine[:i]
			nextLineStart = startOfLine[index:]
			break
		}
	}
	return nextLineStart, thisLineStart
}

func (request *Request) ParseCommand(buffer string) error {
	i := 0
	request.Method = ""
	request.RawURL = ""
	request.ProtocolVersion = ""
	for i = 0; i < len(buffer) && buffer[i] != ' ' && buffer[i] != '\t'; i++ {
		request.Method += string(buffer[i])
	}
	i++;
	for ; i < len(buffer) && buffer[i] != ' ' && buffer[i] != '\t'; i++ {
		request.RawURL += string(buffer[i])
	}
	i++;
	for ; i < len(buffer) && buffer[i] != ' ' && buffer[i] != '\t'; i++ {
		request.ProtocolVersion += string(buffer[i])
	}
	if request.Method == "" || request.RawURL == "" || request.ProtocolVersion == "" {
		log.Printf("Request: %s", buffer)
		return errors.New("Method parse error")
	}
	re := regexp.MustCompile(`^rtsp:\/\/[^:\/]+(:?[:]\d+)?\/(rtsp)\/(.*)`)
	rawURL := re.ReplaceAllString(request.RawURL, "$2://$3")
	var err error
	URL, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	request.URL = URL
	return nil
}

func (request *Request) getHeader(buffer string) (string, string, error) {
	key := ""
	value := ""
	i := 0
	for i = 0; i < len(buffer) && buffer[i] != ':'; i++ {
		key += string(buffer[i])
	}
	i++;
	state := "skip whitespace"
	for ; i < len(buffer); i++ {
		switch state {
		case "skip whitespace":
			if buffer[i] != ' ' && buffer[i] != '\t' && buffer[i] != '\r' && buffer[i] != '\n' {
				value += string(buffer[i])
				state = "value"
			}
		case "value":
			{
				if buffer[i] != '\t' && buffer[i] != '\r' && buffer[i] != '\n' {
					value += string(buffer[i])
					if buffer[i] == ';' {
						state = "skip whitespace"
					}
				}
			}
		}
	}

	return key, value, nil
}

func (request *Request) ParseRequest(buffer string) error {
	nextLineStart, thisLineStart := request.getLine(buffer)
	err := request.ParseCommand(thisLineStart)

	if err != nil {
		return err
	}
	for {
		nextLineStart, thisLineStart = request.getLine(nextLineStart)
		if thisLineStart == "" {
			break
		}
		key, value, err := request.getHeader(thisLineStart)
		if err != nil {
			return err
		}
		request.Headers[key] = value
	}
	return nil
}

func (request *Request) GetURL() *url.URL {
	URL := request.URL

	URL.User = nil
	host := strings.Split(URL.Host, ":")
	if len(host) > 1 && host[1] == "554" {
		URL.Host = host[0]
	}
	return URL
}

func (request *Request) String() string {
	URL := request.GetURL()

	response := fmt.Sprintf("%s %s %s\r\n", request.Method, URL.String(), request.ProtocolVersion)
	for key, value := range request.Headers {
		response += fmt.Sprintf("%s: %s\r\n", key, value)
	}
	response += "\r\n"
	return response
}
