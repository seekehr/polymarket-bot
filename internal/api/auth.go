package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
)

// l2Signature computes the POLY_SIGNATURE header for an authenticated CLOB
// request: HMAC-SHA256 over timestamp + method + path + body, keyed with the
// base64-decoded API secret, encoded as URL-safe base64 with padding. The path
// excludes any query string.
func l2Signature(secret string, timestamp int64, method, path string, body []byte) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%d%s%s", timestamp, method, path)
	mac.Write(body)
	return base64.URLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// decodeSecret accepts the secret in standard or URL-safe base64, padded or not.
func decodeSecret(secret string) ([]byte, error) {
	s := strings.NewReplacer("-", "+", "_", "/").Replace(strings.TrimSpace(secret))
	if n := len(s) % 4; n != 0 {
		s += strings.Repeat("=", 4-n)
	}
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(key) == 0 {
		return nil, fmt.Errorf("api_secret is not valid base64")
	}
	return key, nil
}
