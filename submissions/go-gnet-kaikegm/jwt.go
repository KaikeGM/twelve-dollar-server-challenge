package main

import (
	"bytes"
	"errors"
	"time"

	"github.com/KaikeGM/zerojwt"
)

var (
	jwtVerifier *zerojwt.Verifier
	jwtScratch  *zerojwt.Scratch
	jwtClaims   zerojwt.Claims
)

func setJWTSecret(secret []byte) {
	jwtVerifier = zerojwt.NewVerifier(secret)
	jwtScratch = jwtVerifier.NewScratch()
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

// verifyJWT validates the token using zerojwt's single-threaded Scratch (0 B/op, 0 allocs/op).
func verifyJWT(tok []byte, now int64) (uid int64, username []byte, ok, payloadOK bool) {
	err := jwtVerifier.VerifyWithScratch(tok, now, &jwtClaims, jwtScratch)
	if err != nil {
		if errors.Is(err, zerojwt.ErrInvalidPayload) {
			return 0, nil, true, false
		}
		return 0, nil, false, false
	}
	if !jwtClaims.HasSub || jwtClaims.SubjectID <= 0 || !jwtClaims.HasUsername || len(jwtClaims.Username()) == 0 {
		return 0, nil, true, false
	}
	return jwtClaims.SubjectID, jwtClaims.Username(), true, true
}
