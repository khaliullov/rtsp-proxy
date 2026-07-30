package rtspproxy

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync/atomic"
)

var nonceCounter uint64

// Digest holds parameters for HTTP Digest (and Basic) authentication.
// Supports qop=auth (RFC 2617).
type Digest struct {
	Realm     string
	Nonce     string
	Username  string
	Password  string
	Qop       string // "auth" or empty
	Opaque    string
	Algorithm string
	Nc        uint32 // nonce count (client-side)
}

// NewDigest returns a pointer to a new Digest instance.
func NewDigest() *Digest {
	return &Digest{}
}

// RandomNonce generates a fresh client-side nonce seed (used only as fallback).
func (d *Digest) RandomNonce() {
	n := atomic.AddUint64(&nonceCounter, 1)
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	seed := fmt.Sprintf("%x%d", buf, n)
	h := md5.New()
	io.WriteString(h, seed)
	d.Nonce = hex.EncodeToString(h.Sum(nil))
}

func randomCnonce() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func md5Hex(data string) string {
	h := md5.New()
	io.WriteString(h, data)
	return hex.EncodeToString(h.Sum(nil))
}

// ComputeResponse generates the Digest response for the given method and URI.
// When Qop == "auth" the response includes nc and cnonce (RFC 2617).
func (d *Digest) ComputeResponse(cmd, uri string) (response, ncStr, cnonce string) {
	ha1 := md5Hex(fmt.Sprintf("%s:%s:%s", d.Username, d.Realm, d.Password))
	ha2 := md5Hex(fmt.Sprintf("%s:%s", cmd, uri))

	if strings.EqualFold(d.Qop, "auth") {
		d.Nc++
		ncStr = fmt.Sprintf("%08x", d.Nc)
		cnonce = randomCnonce()
		response = md5Hex(fmt.Sprintf("%s:%s:%s:%s:%s:%s", ha1, d.Nonce, ncStr, cnonce, "auth", ha2))
		return response, ncStr, cnonce
	}

	response = md5Hex(fmt.Sprintf("%s:%s:%s", ha1, d.Nonce, ha2))
	return response, "", ""
}

// AuthorizationHeader stores parsed fields from an Authorization header.
type AuthorizationHeader struct {
	URI      string
	Realm    string
	Nonce    string
	Username string
	Response string
}

// ParseAuthorizationHeader parses an "Authorization: Digest ..." line.
func ParseAuthorizationHeader(buf string) *AuthorizationHeader {
	if buf == "" {
		return nil
	}
	index := strings.Index(buf, "Authorization: Digest ")
	if index < 0 {
		return nil
	}

	var username, realm, nonce, uri, response string
	fields := buf[index+22:]
	for fields != "" {
		var parameter, value string
		n1, _ := fmt.Sscanf(fields, "%[^=]=\"%[^\"]\"", &parameter, &value)
		n2, _ := fmt.Sscanf(fields, "%[^=]=\"\"", &parameter)
		if n1 != 2 && n2 != 1 {
			break
		}
		switch strings.ToLower(parameter) {
		case "username":
			username = value
		case "realm":
			realm = value
		case "nonce":
			nonce = value
		case "uri":
			uri = value
		case "response":
			response = value
		}
		advance := len(parameter) + 2 + len(value) + 1
		if advance > len(fields) {
			break
		}
		fields = fields[advance:]
		for len(fields) > 0 && (fields[0] == ' ' || fields[0] == ',') {
			fields = fields[1:]
		}
		if fields == "" || fields[0] == '\r' || fields[0] == '\n' {
			break
		}
	}

	return &AuthorizationHeader{
		URI:      uri,
		Realm:    realm,
		Nonce:    nonce,
		Username: username,
		Response: response,
	}
}

var (
	digestRe = regexp.MustCompile(`(?i)Digest\s+`)
	basicRe  = regexp.MustCompile(`(?i)Basic\s+realm="([^"]+)"`)

	// Precompiled extractors for common Digest params (quoted + unquoted forms).
	reRealmQ     = regexp.MustCompile(`(?i)realm="([^"]*)"`)
	reRealmU     = regexp.MustCompile(`(?i)realm=([^,\s]+)`)
	reNonceQ     = regexp.MustCompile(`(?i)nonce="([^"]*)"`)
	reNonceU     = regexp.MustCompile(`(?i)nonce=([^,\s]+)`)
	reQopQ       = regexp.MustCompile(`(?i)qop="([^"]*)"`)
	reQopU       = regexp.MustCompile(`(?i)qop=([^,\s]+)`)
	reOpaqueQ    = regexp.MustCompile(`(?i)opaque="([^"]*)"`)
	reOpaqueU    = regexp.MustCompile(`(?i)opaque=([^,\s]+)`)
	reAlgorithmQ = regexp.MustCompile(`(?i)algorithm="([^"]*)"`)
	reAlgorithmU = regexp.MustCompile(`(?i)algorithm=([^,\s]+)`)
)

func extractParam(quoted, unquoted *regexp.Regexp, s string) string {
	if m := quoted.FindStringSubmatch(s); len(m) == 2 {
		return m[1]
	}
	if m := unquoted.FindStringSubmatch(s); len(m) == 2 {
		return m[1]
	}
	return ""
}

// ParseWWWAuthenticate extracts Digest/Basic parameters from a WWW-Authenticate header value.
func ParseWWWAuthenticate(paramsStr string) (realm, nonce, qop, opaque, algorithm string, isDigest bool) {
	if paramsStr == "" {
		return
	}

	if !digestRe.MatchString(paramsStr) {
		if m := basicRe.FindStringSubmatch(paramsStr); len(m) == 2 {
			realm = m[1]
		}
		return
	}
	isDigest = true

	realm = extractParam(reRealmQ, reRealmU, paramsStr)
	nonce = extractParam(reNonceQ, reNonceU, paramsStr)
	qop = extractParam(reQopQ, reQopU, paramsStr)
	if strings.Contains(qop, ",") {
		for _, part := range strings.Split(qop, ",") {
			part = strings.TrimSpace(part)
			if part == "auth" {
				qop = "auth"
				break
			}
		}
	}
	opaque = extractParam(reOpaqueQ, reOpaqueU, paramsStr)
	algorithm = extractParam(reAlgorithmQ, reAlgorithmU, paramsStr)
	if algorithm == "" {
		algorithm = "MD5"
	}
	return
}

// NcString formats a nonce-count as 8-digit hex.
func NcString(nc uint32) string {
	return fmt.Sprintf("%08x", nc)
}
