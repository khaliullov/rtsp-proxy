package rtspproxy

import (
	"container/list"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Request represents an RTSP request.
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

// NewRequest creates a new RTSP request.
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

// NewRequestFromBuffer creates a new RTSP request from a buffer.
func NewRequestFromBuffer(buffer string) (*Request, error) {
	request := &Request{
		Headers:       make(map[string]string),
		Subscriptions: list.New(),
	}
	if buffer != "" {
		err := request.ParseRequest(buffer)
		if err != nil {
			return nil, err
		}
	}
	Logf("DEBUG: NewRequestFromBuffer created request.URL: %+v", request.URL)
	return request, nil
}

// ParseCommand parses the command line of an RTSP request.
func (request *Request) ParseCommand(buffer string) error {
	i := 0
	request.Method = ""
	request.RawURL = ""
	request.ProtocolVersion = ""
	for i = 0; i < len(buffer) && buffer[i] != ' ' && buffer[i] != '\t'; i++ {
		request.Method += string(buffer[i])
	}
	i++
	for ; i < len(buffer) && buffer[i] != ' ' && buffer[i] != '\t'; i++ {
		request.RawURL += string(buffer[i])
	}
	i++
	for ; i < len(buffer) && buffer[i] != ' ' && buffer[i] != '\t'; i++ {
		request.ProtocolVersion += string(buffer[i])
	}
	if request.Method == "" || request.RawURL == "" || request.ProtocolVersion == "" {
		LogCriticalf("Request: %s, length: %d", buffer, len(buffer))
		return errors.New("Method parse error")
	}
	// Proxy URL form: rtsp://proxy/rtsp/host/path  →  rtsp://host/path
	re := regexp.MustCompile(`^rtsp:\/\/[^:\/]+(:?[:]\d+)?\/(rtsp)\/(.*)`)
	rawURL := re.ReplaceAllString(request.RawURL, "$2://$3")
	Logf("DEBUG: ParseCommand rawURL after regex: %q", rawURL)
	URL, err := url.Parse(rawURL)
	if err != nil {
		LogCriticalf("Failed to parse URL %q: %v", rawURL, err)
		return err
	}
	request.URL = URL
	Logf("DEBUG: ParseCommand parsed request.URL: %+v", request.URL)
	return nil
}

// ParseRequest parses an entire RTSP request from a buffer.
func (request *Request) ParseRequest(buffer string) error {
	next, thisLine := sharedLineSplit(buffer)
	err := request.ParseCommand(thisLine)
	if err != nil {
		LogCriticalf("Failed to parse request: %s, length: %d", buffer, len(buffer))
		return err
	}
	for {
		next, thisLine = sharedLineSplit(next)
		if thisLine == "" {
			break
		}
		key, value, err := sharedParseHeader(thisLine)
		if err != nil {
			return err
		}
		request.Headers[key] = value
	}
	return nil
}

// GetURL returns the URL of the request, with default RTSP port removed if present.
func (request *Request) GetURL() *url.URL {
	URL := *request.URL // shallow copy
	URL.User = nil
	host := strings.Split(URL.Host, ":")
	if len(host) > 1 && host[1] == "554" {
		URL.Host = host[0]
	}
	return &URL
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
