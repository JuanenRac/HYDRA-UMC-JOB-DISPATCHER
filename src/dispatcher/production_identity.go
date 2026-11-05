// HYDRA-UMC-JOB-DISPATCHER - identity of the production events
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The production reports accept the signed token of HYDRA-UMC-SERVER as the identity of a caller (JWT, HS256, claims username, role and exp). When the operator
// gives the signing key in a file, the dispatcher signs a short-lived token of its own for every event it sends, so nothing that expires has to be pasted
// into a setting. The key is read once, is never printed and never leaves the machine; the token has the role operator, which is what a POST needs.
package dispatcher

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// MinIdentityKeyLength is the shortest signing key the family accepts.
const MinIdentityKeyLength = 32

// identityTokenLifetime is how long a token that the dispatcher signs is valid: long enough for one request, short enough to be worth nothing afterwards.
const identityTokenLifetime = 60 * time.Second

// LoadIdentityKey reads the signing key from a file; the error never holds the key.
func LoadIdentityKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("cannot read the identity key file")
	}
	key := strings.TrimSpace(string(raw))
	if len(key) < MinIdentityKeyLength {
		return nil, fmt.Errorf("the identity key must have at least %d characters", MinIdentityKeyLength)
	}
	return []byte(key), nil
}

// SignIdentityToken makes a token in the format of the SERVER, valid for identityTokenLifetime from now.
func SignIdentityToken(key []byte, username, role string, now time.Time) string {
	part := func(v any) string {
		data, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(data)
	}
	head := part(map[string]string{"alg": "HS256", "typ": "JWT"})
	body := part(map[string]any{"username": username, "role": role, "exp": now.Add(identityTokenLifetime).Unix()})
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(head + "." + body))
	return head + "." + body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// SignedHTTPSink posts every event like HTTPSink, presenting a token signed with the identity key (role operator) that is made again for every request.
func SignedHTTPSink(url string, key []byte, timeout time.Duration) ProductionEventSink {
	return httpSink(url, func() string { return SignIdentityToken(key, "job-dispatcher", "operator", time.Now()) }, timeout)
}
