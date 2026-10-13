package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"
	"time"
)

func signJWT(payload string, secret []byte) string {
	enc := base64.RawURLEncoding
	h := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	p := enc.EncodeToString([]byte(payload))
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(h + "." + p))
	s := enc.EncodeToString(mac.Sum(nil))
	return h + "." + p + "." + s
}

func TestJWT(t *testing.T) {
	secret := []byte("twelve-dollar-challenge")
	setJWTSecret(secret)
	now := time.Now().Unix()

	// 1. Valid token
	validTok := []byte(signJWT(fmt.Sprintf(`{"sub":"123","username":"alice","iat":%d,"exp":%d}`, now, now+3600), secret))
	uid, user, ok, plOK := verifyJWT(validTok, now)
	if !ok || !plOK || uid != 123 || string(user) != "alice" {
		t.Fatalf("expected valid token, got uid=%d user=%s ok=%v plOK=%v", uid, user, ok, plOK)
	}

	// 2. Expired token
	expTok := []byte(signJWT(fmt.Sprintf(`{"sub":"123","username":"alice","iat":%d,"exp":%d}`, now-7200, now-3600), secret))
	_, _, ok, _ = verifyJWT(expTok, now)
	if ok {
		t.Fatalf("expected expired token to fail")
	}

	// 3. Sub not integer string (e.g. "abc")
	badSubTok := []byte(signJWT(fmt.Sprintf(`{"sub":"abc","username":"alice","iat":%d,"exp":%d}`, now, now+3600), secret))
	_, _, ok, plOK = verifyJWT(badSubTok, now)
	if !ok || plOK {
		t.Fatalf("expected bad sub to have ok=true, plOK=false, got ok=%v plOK=%v", ok, plOK)
	}

	// 4. Sub is a number, not a string: {"sub":123}
	numSubTok := []byte(signJWT(fmt.Sprintf(`{"sub":123,"username":"alice","iat":%d,"exp":%d}`, now, now+3600), secret))
	_, _, ok, plOK = verifyJWT(numSubTok, now)
	if !ok || plOK {
		t.Fatalf("expected numeric sub to have ok=true, plOK=false, got ok=%v plOK=%v", ok, plOK)
	}

	// 5. No username
	noNameTok := []byte(signJWT(fmt.Sprintf(`{"sub":"123","iat":%d,"exp":%d}`, now, now+3600), secret))
	_, _, ok, plOK = verifyJWT(noNameTok, now)
	if !ok || plOK {
		t.Fatalf("expected no name to have ok=true, plOK=false, got ok=%v plOK=%v", ok, plOK)
	}

	// 6. Bad signature
	badSigTok := append(validTok[:len(validTok)-4], "AAAA"...)
	_, _, ok, _ = verifyJWT(badSigTok, now)
	if ok {
		t.Fatalf("expected bad signature to fail")
	}

	// 7. Alg none
	enc := base64.RawURLEncoding
	noneH := enc.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	noneP := enc.EncodeToString([]byte(fmt.Sprintf(`{"sub":"123","username":"alice","exp":%d}`, now+3600)))
	noneTok := []byte(noneH + "." + noneP + ".")
	_, _, ok, _ = verifyJWT(noneTok, now)
	if ok {
		t.Fatalf("expected alg:none to fail")
	}
}

func BenchmarkVerifyJWT(b *testing.B) {
	secret := []byte("twelve-dollar-challenge")
	setJWTSecret(secret)
	now := time.Now().Unix()
	validTok := []byte(signJWT(fmt.Sprintf(`{"sub":"123","username":"alice","iat":%d,"exp":%d}`, now, now+3600), secret))

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _, ok, plOK := verifyJWT(validTok, now)
		if !ok || !plOK {
			b.Fatal("failed")
		}
	}
}
