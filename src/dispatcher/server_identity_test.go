// HYDRA-UMC-JOB-DISPATCHER - tests of the identity of the SERVER as the caller
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func encodedPart(v any) string {
	data, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(data)
}

// tokenWith signs claims with key and the given algorithm in the header.
func tokenWith(key []byte, alg string, claims map[string]any) string {
	head := encodedPart(map[string]string{"alg": alg, "typ": "JWT"})
	body := encodedPart(claims)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(head + "." + body))
	return head + "." + body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func goodClaims(role string, now time.Time) map[string]any {
	return map[string]any{"username": "ana", "role": role, "exp": now.Add(10 * time.Minute).Unix()}
}

func TestVerifyServerTokenGivesTheUserAndTheRole(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	identity, err := VerifyServerToken(tokenWith(testIdentityKey, "HS256", goodClaims("operator", now)), testIdentityKey, now)
	if err != nil {
		t.Fatalf("a good token was refused: %v", err)
	}
	if identity.Username != "ana" || identity.Role != "operator" || !identity.CanWrite() {
		t.Fatalf("identity = %+v", identity)
	}
	viewer, err := VerifyServerToken(tokenWith(testIdentityKey, "HS256", goodClaims("viewer", now)), testIdentityKey, now)
	if err != nil || viewer.CanWrite() {
		t.Fatalf("a viewer must read only: %+v %v", viewer, err)
	}
}

func TestVerifyServerTokenTokenOfOurOwnSigningIsAccepted(t *testing.T) {
	now := time.Now()
	if _, err := VerifyServerToken(SignIdentityToken(testIdentityKey, "job-dispatcher", "operator", now), testIdentityKey, now); err != nil {
		t.Fatalf("the token that the dispatcher signs must verify: %v", err)
	}
}

func TestVerifyServerTokenRefusesEveryDefect(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	other := []byte("another-key-of-more-than-32-characters!")
	later := func(extra map[string]any) map[string]any {
		claims := goodClaims("admin", now)
		for k, v := range extra {
			claims[k] = v
		}
		return claims
	}
	cases := map[string]string{
		"none algorithm":      tokenWith(testIdentityKey, "none", goodClaims("admin", now)),
		"other algorithm":     tokenWith(testIdentityKey, "HS512", goodClaims("admin", now)),
		"wrong key":           tokenWith(other, "HS256", goodClaims("admin", now)),
		"expired":             tokenWith(testIdentityKey, "HS256", later(map[string]any{"exp": now.Add(-5 * time.Second).Unix()})),
		"no expiry":           tokenWith(testIdentityKey, "HS256", map[string]any{"username": "ana", "role": "admin"}),
		"text expiry":         tokenWith(testIdentityKey, "HS256", later(map[string]any{"exp": "tomorrow"})),
		"not yet":             tokenWith(testIdentityKey, "HS256", later(map[string]any{"nbf": now.Add(5 * time.Minute).Unix()})),
		"issued in future":    tokenWith(testIdentityKey, "HS256", later(map[string]any{"iat": now.Add(5 * time.Minute).Unix()})),
		"no user":             tokenWith(testIdentityKey, "HS256", later(map[string]any{"username": ""})),
		"unknown role":        tokenWith(testIdentityKey, "HS256", later(map[string]any{"role": "root"})),
		"not a token":         "abc",
		"empty parts":         "..",
		"empty":               "",
		"too long":            strings.Repeat("a.", 3000),
		"signature truncated": strings.TrimSuffix(tokenWith(testIdentityKey, "HS256", goodClaims("admin", now)), "A") + "!",
	}
	for label, token := range cases {
		if _, err := VerifyServerToken(token, testIdentityKey, now); err == nil {
			t.Errorf("%s: the token was accepted", label)
		}
	}
}

func TestVerifyServerTokenReasonsNameNoSecret(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	_, err := VerifyServerToken(tokenWith([]byte("another-key-of-more-than-32-characters!"), "HS256", goodClaims("admin", now)), testIdentityKey, now)
	if err == nil || strings.Contains(err.Error(), string(testIdentityKey)) {
		t.Fatalf("the reason must exist and name no key: %v", err)
	}
}
