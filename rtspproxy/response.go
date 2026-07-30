package rtspproxy

import (
	"errors"
	"fmt"
	"strconv"
)

// Response represents an RTSP response.
type Response struct {
	Status          string
	ProtocolVersion string
	Code            int
	Headers         map[string]string
	Body            string
}

// NewResponse creates a new RTSP response.
func NewResponse(code int, args ...string) (*Response, error) {
	status := "OK"
	protocolVersion := "RTSP/1.0"
	if len(args) > 0 {
		status = args[0]
	}
	if len(args) > 1 {
		protocolVersion = args[1]
	}
	response := &Response{
		Code:            code,
		ProtocolVersion: protocolVersion,
		Status:          status,
		Headers:         make(map[string]string),
	}
	return response, nil
}

// NewResponseFromBuffer creates a new RTSP response from a buffer.
func NewResponseFromBuffer(buffer string) (*Response, error) {
	response, _ := NewResponse(400, "Bad request")
	if buffer != "" {
		err := response.ParseResponse(buffer)
		if err != nil {
			return nil, err
		}
	}
	return response, nil
}

// ParseStatus parses the status line of an RTSP response.
func (response *Response) ParseStatus(buffer string) error {
	i := 0
	response.Status = ""
	response.Code = 0
	response.ProtocolVersion = ""
	for i = 0; i < len(buffer) && buffer[i] != ' ' && buffer[i] != '\t'; i++ {
		response.ProtocolVersion += string(buffer[i])
	}
	i++
	code := ""
	for ; i < len(buffer) && buffer[i] != ' ' && buffer[i] != '\t'; i++ {
		code += string(buffer[i])
	}
	if len(code) == 3 {
		var err error
		response.Code, err = strconv.Atoi(code)
		if err != nil {
			return err
		}
	}
	i++
	for ; i < len(buffer) && buffer[i] != '\r' && buffer[i] != '\n'; i++ {
		response.Status += string(buffer[i])
	}
	if response.Status == "" || response.Code == 0 || response.ProtocolVersion == "" {
		return errors.New("Status parse error")
	}
	return nil
}

// ParseResponse parses an entire RTSP response from a buffer.
func (response *Response) ParseResponse(buffer string) error {
	next, thisLine := sharedLineSplit(buffer)
	err := response.ParseStatus(thisLine)
	if err != nil {
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
		response.Headers[key] = value
	}
	if contentLengthRaw := headerGet(response.Headers, "Content-Length"); contentLengthRaw != "" {
		contentLength, _ := strconv.Atoi(contentLengthRaw)
		if contentLength > 0 {
			if contentLength <= len(next) {
				response.Body = next[:contentLength]
			} else {
				response.Body = next
			}
		}
		return nil
	}
	return nil
}

// String returns the string representation of the RTSP response.
func (response *Response) String() string {
	res := fmt.Sprintf("%s %d %s\r\n", response.ProtocolVersion, response.Code, response.Status)
	for key, value := range response.Headers {
		res += fmt.Sprintf("%s: %s\r\n", key, value)
	}
	res += "\r\n"
	if response.Body != "" {
		res += response.Body
	}
	return res
}
