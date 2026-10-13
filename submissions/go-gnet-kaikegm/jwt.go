package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"hash"
	"sync"
	"time"
)

var (
	jwtSecret []byte
	hmacPool  = sync.Pool{
		New: func() any {
			return hmac.New(sha256.New, jwtSecret)
		},
	}
)

func setJWTSecret(secret []byte) {
	jwtSecret = secret
	hmacPool = sync.Pool{
		New: func() any {
			return hmac.New(sha256.New, jwtSecret)
		},
	}
}

func authenticate(authHeader []byte) (uid int64, username []byte, status int, msg string) {
	if !bytes.HasPrefix(authHeader, []byte("Bearer ")) {
		return 0, nil, 401, "missing bearer token"
	}
	token := authHeader[len("Bearer "):]
	uid, username, ok, payloadOK := verifyJWT(token, time.Now().Unix())
	if !ok {
		return 0, nil, 401, "invalid or expired token"
	}
	if !payloadOK {
		return 0, nil, 401, "invalid token payload"
	}
	return uid, username, 0, ""
}

// verifyJWT validates the token without heap allocations on the hot path.
func verifyJWT(tok []byte, now int64) (uid int64, username []byte, ok, payloadOK bool) {
	// 1. Split header.payload.signature using bytes.Cut (0 allocations)
	hBytes, rest, hasDot1 := bytes.Cut(tok, []byte("."))
	if !hasDot1 {
		return 0, nil, false, false
	}
	pBytes, sBytes, hasDot2 := bytes.Cut(rest, []byte("."))
	if !hasDot2 || bytes.IndexByte(sBytes, '.') >= 0 {
		return 0, nil, false, false
	}

	enc := base64.RawURLEncoding

	// Decode signature (must be 32 bytes for HS256)
	var sigBuf [48]byte
	nSig, err := enc.Decode(sigBuf[:], sBytes)
	if err != nil || nSig != 32 {
		return 0, nil, false, false
	}

	// 2. Decode header (stack buffer)
	var hdrBuf [128]byte
	if enc.DecodedLen(len(hBytes)) > len(hdrBuf) {
		return 0, nil, false, false
	}
	nHdr, err := enc.Decode(hdrBuf[:], hBytes)
	if err != nil || nHdr == 0 {
		return 0, nil, false, false
	}
	hdr := hdrBuf[:nHdr]

	// Header check: must have "alg":"HS256" and not alg=none or others
	if !isHS256Header(hdr) {
		return 0, nil, false, false
	}

	// 3. Verify HMAC signature using pooled hasher
	mac := hmacPool.Get().(hash.Hash)
	mac.Reset()
	_, _ = mac.Write(hBytes)
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(pBytes)
	var expectedSig [32]byte
	calculated := mac.Sum(expectedSig[:0])
	hmacPool.Put(mac)

	if !hmac.Equal(sigBuf[:32], calculated) {
		return 0, nil, false, false
	}

	// 4. Decode payload (stack buffer up to 512 bytes)
	var plBuf [512]byte
	var plBytes []byte
	decLen := enc.DecodedLen(len(pBytes))
	if decLen <= len(plBuf) {
		nPl, err := enc.Decode(plBuf[:], pBytes)
		if err != nil || nPl == 0 {
			return 0, nil, false, false
		}
		plBytes = plBuf[:nPl]
	} else {
		// Fallback for unusually large payload
		var err error
		plBytes, err = enc.DecodeString(string(pBytes))
		if err != nil || len(plBytes) == 0 {
			return 0, nil, false, false
		}
	}

	// 5. Parse claims (sub, username, exp, nbf)
	uid, username, expValid, nbfValid, plValid := parseJWTClaims(plBytes, now)
	if !expValid || !nbfValid {
		return 0, nil, false, false
	}
	if !plValid {
		return 0, nil, true, false
	}

	return uid, username, true, true
}

// isHS256Header validates that alg is HS256 and only HS256.
func isHS256Header(h []byte) bool {
	idx := bytes.Index(h, []byte(`"alg"`))
	if idx < 0 {
		return false
	}
	rest := bytes.TrimSpace(h[idx+5:])
	if len(rest) == 0 || rest[0] != ':' {
		return false
	}
	rest = bytes.TrimSpace(rest[1:])
	return bytes.HasPrefix(rest, []byte(`"HS256"`))
}

// parseJWTClaims parses sub (positive int), username, exp and nbf with 0 heap allocations.
func parseJWTClaims(b []byte, now int64) (uid int64, username []byte, expValid, nbfValid, payloadValid bool) {
	expValid = true
	nbfValid = true

	// Parse exp
	if expIdx := bytes.Index(b, []byte(`"exp"`)); expIdx >= 0 {
		rest := bytes.TrimSpace(b[expIdx+5:])
		if len(rest) > 0 && rest[0] == ':' {
			rest = bytes.TrimSpace(rest[1:])
			expSec, ok := parseNumeric(rest)
			if ok && float64(now) >= expSec {
				expValid = false
				return 0, nil, false, true, false
			}
		}
	}

	// Parse nbf
	if nbfIdx := bytes.Index(b, []byte(`"nbf"`)); nbfIdx >= 0 {
		rest := bytes.TrimSpace(b[nbfIdx+5:])
		if len(rest) > 0 && rest[0] == ':' {
			rest = bytes.TrimSpace(rest[1:])
			nbfSec, ok := parseNumeric(rest)
			if ok && float64(now) < nbfSec {
				nbfValid = false
				return 0, nil, true, false, false
			}
		}
	}

	// Parse sub: MUST be a quoted string of positive integer, not a raw number!
	subIdx := bytes.Index(b, []byte(`"sub"`))
	if subIdx < 0 {
		return 0, nil, true, true, false
	}
	rest := bytes.TrimSpace(b[subIdx+5:])
	if len(rest) == 0 || rest[0] != ':' {
		return 0, nil, true, true, false
	}
	rest = bytes.TrimSpace(rest[1:])
	if len(rest) == 0 || rest[0] != '"' {
		return 0, nil, true, true, false
	}
	rest = rest[1:]
	endQuote := bytes.IndexByte(rest, '"')
	if endQuote <= 0 || endQuote > 18 {
		return 0, nil, true, true, false
	}
	subStr := rest[:endQuote]
	var parsedUID int64
	for i := 0; i < len(subStr); i++ {
		c := subStr[i]
		if c < '0' || c > '9' {
			return 0, nil, true, true, false
		}
		parsedUID = parsedUID*10 + int64(c-'0')
	}
	if parsedUID <= 0 {
		return 0, nil, true, true, false
	}

	// Parse username: MUST be a string
	userIdx := bytes.Index(b, []byte(`"username"`))
	if userIdx < 0 {
		return 0, nil, true, true, false
	}
	rest = bytes.TrimSpace(b[userIdx+10:])
	if len(rest) == 0 || rest[0] != ':' {
		return 0, nil, true, true, false
	}
	rest = bytes.TrimSpace(rest[1:])
	if len(rest) == 0 || rest[0] != '"' {
		return 0, nil, true, true, false
	}
	rest = rest[1:]
	endUserQuote := bytes.IndexByte(rest, '"')
	if endUserQuote < 0 {
		return 0, nil, true, true, false
	}
	userBytes := rest[:endUserQuote]

	return parsedUID, userBytes, true, true, true
}

func parseNumeric(b []byte) (float64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	i := 0
	for i < len(b) && (b[i] >= '0' && b[i] <= '9') {
		i++
	}
	if i == 0 {
		return 0, false
	}
	var val int64
	for j := 0; j < i; j++ {
		val = val*10 + int64(b[j]-'0')
	}
	// Check for decimal part if any
	if i < len(b) && b[i] == '.' {
		i++
		var frac int64
		var div float64 = 1
		for i < len(b) && (b[i] >= '0' && b[i] <= '9') {
			frac = frac*10 + int64(b[i]-'0')
			div *= 10
			i++
		}
		return float64(val) + float64(frac)/div, true
	}
	return float64(val), true
}
