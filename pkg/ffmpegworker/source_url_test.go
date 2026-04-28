package ffmpegworker

import (
	"errors"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestSourceURLVerifyBindsClaims(t *testing.T) {
	base, err := url.Parse("https://cloudreve.example.com")
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	claims := SourceURLClaims{TaskID: 12, FileID: 34, EntityID: 56, Expires: time.Now().Add(time.Minute).Unix(), Nonce: "nonce"}

	raw, err := BuildSourceURL(base, "/api/v4/video/worker/source/12", claims, "secret")
	if err != nil {
		t.Fatalf("BuildSourceURL: %v", err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse built: %v", err)
	}

	got, err := VerifySourceURL(12, parsed.Query(), "secret", time.Now())
	if err != nil {
		t.Fatalf("VerifySourceURL: %v", err)
	}
	if got.FileID != claims.FileID || got.EntityID != claims.EntityID || got.Nonce != claims.Nonce {
		t.Fatalf("claims mismatch: %+v", got)
	}

	q := parsed.Query()
	q.Set(QueryEntityID, "57")
	if _, err := VerifySourceURL(12, q, "secret", time.Now()); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected invalid token after tamper, got %v", err)
	}
	if _, err := VerifySourceURL(13, parsed.Query(), "secret", time.Now()); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected invalid token for different task id, got %v", err)
	}
}

func TestSourceURLVerifyExpired(t *testing.T) {
	claims := SourceURLClaims{TaskID: 1, FileID: 2, EntityID: 3, Expires: time.Now().Add(-time.Minute).Unix(), Nonce: "nonce"}
	values := url.Values{}
	values.Set(QueryFileID, "2")
	values.Set(QueryEntityID, "3")
	values.Set(QueryExpires, strconv.FormatInt(claims.Expires, 10))
	values.Set(QueryNonce, claims.Nonce)
	values.Set(QuerySignature, SignSourceURL(claims, "secret"))

	if _, err := VerifySourceURL(1, values, "secret", time.Now()); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("expected expired token, got %v", err)
	}
}
