package rtspproxy

import (
	"errors"
	"fmt"
	"strconv"
)

type Response struct {
	Status	 		string
	ProtocolVersion string
	Code			int
	Headers			map[string]string
	Body			string
}

func NewResponse(buffer string) (*Response, error) {
	response := &Response{Headers: make(map[string]string)}
	if buffer != "" {
		err := response.ParseResponse(buffer)
		if err != nil {
			return nil, err
		}
	}
	return response, nil
}

func (response *Response) getLine(startOfLine string) (thisLineStart, nextLineStart string) {
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

func (response *Response) ParseStatus(buffer string) error {
	i := 0
	response.Status = ""
	response.Code = 0
	response.ProtocolVersion = ""
	for i = 0; i < len(buffer) && buffer[i] != ' ' && buffer[i] != '\t'; i++ {
		response.ProtocolVersion += string(buffer[i])
	}
	i++;
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
	i++;
	for ; i < len(buffer) && buffer[i] != '\r' && buffer[i] != '\n'; i++ {
		response.Status += string(buffer[i])
	}
	if response.Status == "" || response.Code == 0 || response.ProtocolVersion == "" {
		return errors.New("Status parse error")
	}
	return nil
}

func (response *Response) getHeader(buffer string) (string, string, error) {
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

func (response *Response) ParseResponse(buffer string) error {
	nextLineStart, thisLineStart := response.getLine(buffer)
	err := response.ParseStatus(thisLineStart)

	if err != nil {
		return err
	}
	for {
		nextLineStart, thisLineStart = response.getLine(nextLineStart)
		if thisLineStart == "" {
			break
		}
		key, value, err := response.getHeader(thisLineStart)
		if err != nil {
			return err
		}
		response.Headers[key] = value
	}
	response.Body = nextLineStart
	return nil
}

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

