package rtspproxy

import (
	"errors"
	"strconv"
	"strings"
)

// sharedLineSplit splits the first line from buf.
// Accepts \r\n, \r or \n as line endings.
// Returns (rest, line) — same order as the original getLine helpers.
func sharedLineSplit(buf string) (rest, line string) {
	for i := 0; i < len(buf); i++ {
		c := buf[i]
		if c == '\r' || c == '\n' {
			line = buf[:i]
			if c == '\r' && i+1 < len(buf) && buf[i+1] == '\n' {
				rest = buf[i+2:]
			} else {
				rest = buf[i+1:]
			}
			return rest, line
		}
	}
	return "", buf
}

// sharedParseHeader parses a single "Key: Value" header line.
// Semicolon-separated parameters are preserved in the value (whitespace after ';' is skipped).
// canonicalHeaderKey returns a canonical MIME header key
// (e.g. "content-length" -> "Content-Length").
func canonicalHeaderKey(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	b := []byte(s)
	upper := true
	for i, c := range b {
		if upper && c >= 'a' && c <= 'z' {
			b[i] = c - 32
		} else if !upper && c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
		upper = c == '-'
	}
	return string(b)
}

// headerGet retrieves a header value case-insensitively.
func headerGet(headers map[string]string, key string) string {
	if v, ok := headers[key]; ok {
		return v
	}
	canon := canonicalHeaderKey(key)
	if v, ok := headers[canon]; ok {
		return v
	}
	// brute-force fallback
	lower := strings.ToLower(key)
	for k, v := range headers {
		if strings.ToLower(k) == lower {
			return v
		}
	}
	return ""
}

func sharedParseHeader(line string) (key, value string, err error) {
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return "", "", errors.New("header parse error: missing colon")
	}
	key = canonicalHeaderKey(line[:colon])
	i := colon + 1
	// skip leading whitespace
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	var b strings.Builder
	state := "value"
	for ; i < len(line); i++ {
		c := line[i]
		switch state {
		case "value":
			if c == '\t' || c == '\r' || c == '\n' {
				continue
			}
			b.WriteByte(c)
			if c == ';' {
				state = "skip"
			}
		case "skip":
			if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
				b.WriteByte(c)
				state = "value"
			}
		}
	}
	return key, b.String(), nil
}

// sharedParseContentLength extracts Content-Length from a header block (bytes).
// Optimized to avoid allocations.
func sharedParseContentLength(headerPart []byte) int {
	needle := []byte("content-length:")
	nLen := len(needle)

	for i := 0; i+nLen <= len(headerPart); i++ {
		// Case-insensitive match for "content-length:"
		match := true
		for j := 0; j < nLen; j++ {
			c1 := headerPart[i+j]
			c2 := needle[j]
			if c1 >= 'A' && c1 <= 'Z' {
				c1 += 32
			}
			if c1 != c2 {
				match = false
				break
			}
		}

		if match {
			rest := headerPart[i+nLen:]
			lineEnd := indexBytes(rest, []byte("\r\n"))
			if lineEnd < 0 {
				lineEnd = len(rest)
			}
			clStr := strings.TrimSpace(string(rest[:lineEnd]))
			n, _ := strconv.Atoi(clStr)
			return n
		}
	}
	return 0
}

func indexBytes(haystack, needle []byte) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
