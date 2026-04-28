package ffmpegworker

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	QueryFileID    = "file_id"
	QueryEntityID  = "entity_id"
	QueryExpires   = "expires"
	QueryNonce     = "nonce"
	QuerySignature = "signature"
)

var (
	ErrMissingSecret = errors.New("missing worker source secret")
	ErrInvalidToken  = errors.New("invalid worker source token")
	ErrExpiredToken  = errors.New("expired worker source token")
)

type SourceURLClaims struct {
	TaskID   int
	FileID   int
	EntityID int
	Expires  int64
	Nonce    string
}

func BuildSourceURL(base *url.URL, path string, claims SourceURLClaims, secret string) (string, error) {
	if base == nil {
		return "", fmt.Errorf("missing source url base")
	}
	if strings.TrimSpace(secret) == "" {
		return "", ErrMissingSecret
	}

	u := *base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = ""
	u.Fragment = ""

	q := u.Query()
	q.Set(QueryFileID, strconv.Itoa(claims.FileID))
	q.Set(QueryEntityID, strconv.Itoa(claims.EntityID))
	q.Set(QueryExpires, strconv.FormatInt(claims.Expires, 10))
	q.Set(QueryNonce, claims.Nonce)
	q.Set(QuerySignature, SignSourceURL(claims, secret))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func VerifySourceURL(taskID int, values url.Values, secret string, now time.Time) (SourceURLClaims, error) {
	claims := SourceURLClaims{TaskID: taskID}
	if strings.TrimSpace(secret) == "" {
		return claims, ErrMissingSecret
	}

	var err error
	if claims.FileID, err = strconv.Atoi(values.Get(QueryFileID)); err != nil || claims.FileID <= 0 {
		return claims, ErrInvalidToken
	}
	if claims.EntityID, err = strconv.Atoi(values.Get(QueryEntityID)); err != nil || claims.EntityID <= 0 {
		return claims, ErrInvalidToken
	}
	if claims.Expires, err = strconv.ParseInt(values.Get(QueryExpires), 10, 64); err != nil || claims.Expires <= 0 {
		return claims, ErrInvalidToken
	}
	if now.Unix() > claims.Expires {
		return claims, ErrExpiredToken
	}

	claims.Nonce = strings.TrimSpace(values.Get(QueryNonce))
	if claims.Nonce == "" {
		return claims, ErrInvalidToken
	}

	expected := SignSourceURL(claims, secret)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(values.Get(QuerySignature))) != 1 {
		return claims, ErrInvalidToken
	}

	return claims, nil
}

func SignSourceURL(claims SourceURLClaims, secret string) string {
	payload := fmt.Sprintf("%d\n%d\n%d\n%d\n%s", claims.TaskID, claims.FileID, claims.EntityID, claims.Expires, claims.Nonce)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
