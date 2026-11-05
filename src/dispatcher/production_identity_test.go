// HYDRA-UMC-JOB-DISPATCHER - tests of the identity of the production events
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testIdentityKey = []byte("0123456789abcdef0123456789abcdef")

func TestSignedTokenHasTheFormatTheServicesVerify(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	parts := strings.Split(SignIdentityToken(testIdentityKey, "job-dispatcher", "operator", now), ".")
	if len(parts) != 3 {
		t.Fatalf("a JWT has three parts, got %d", len(parts))
	}
	var head map[string]string
	var claims struct {
		Username string `json:"username"`
		Role     string `json:"role"`
		Exp      int64  `json:"exp"`
	}
	for i, target := range []any{&head, &claims} {
		raw, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, target); err != nil {
			t.Fatal(err)
		}
	}
	if head["alg"] != "HS256" || claims.Username != "job-dispatcher" || claims.Role != "operator" || claims.Exp != now.Add(60*time.Second).Unix() {
		t.Fatalf("unexpected token: %v %+v", head, claims)
	}
	mac := hmac.New(sha256.New, testIdentityKey)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) != parts[2] {
		t.Fatal("the signature must be HMAC-SHA256 of head.body with the key")
	}
}

func TestSignedHTTPSinkPresentsAFreshToken(t *testing.T) {
	seen := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	valid := readFixture(t, "production-event.valid.json")
	if err := SignedHTTPSink(server.URL, testIdentityKey, time.Second)(valid); err != nil {
		t.Fatal(err)
	}
	got := <-seen
	if !strings.HasPrefix(got, "Bearer ") || strings.Count(got, ".") != 2 {
		t.Fatalf("a signed bearer token was expected, got %q", got)
	}
}

func TestLoadIdentityKey(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.key")
	short := filepath.Join(dir, "short.key")
	if err := os.WriteFile(good, append(testIdentityKey, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(short, []byte("too short"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := LoadIdentityKey(good)
	if err != nil || string(key) != string(testIdentityKey) {
		t.Fatalf("a good key must load without its line end: %v", err)
	}
	if _, err := LoadIdentityKey(short); err == nil || strings.Contains(err.Error(), "too short") {
		t.Fatalf("a short key must be refused without showing it: %v", err)
	}
	if _, err := LoadIdentityKey(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing file must be an error")
	}
}
