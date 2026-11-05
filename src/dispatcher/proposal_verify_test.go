// HYDRA-UMC-JOB-DISPATCHER - tests of the check of a signed trajectory proposal
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testProposalKey = "0123456789abcdef0123456789abcdef-key"

func proposalKeyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "identity.key")
	if err := os.WriteFile(path, []byte(testProposalKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestVerifier(t *testing.T, serverURL string) *ProposalVerifier {
	t.Helper()
	v, err := NewProposalVerifier(ProposalVerifyConfig{Enabled: true, ServerURL: serverURL, IdentityKeyFile: proposalKeyFile(t), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewProposalVerifier: %v", err)
	}
	return v
}

func TestProposalVerify_AskedWithTheRobotAndTheSignedIdentity(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"valid":true,"reason":"ok","key_id":"k1"}`))
	}))
	defer srv.Close()
	verdict, err := newTestVerifier(t, srv.URL).Verify(context.Background(), json.RawMessage(`{"signature":"s","robot_id":"7"}`), "7")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !verdict.Valid || verdict.KeyID != "k1" {
		t.Errorf("verdict %+v, want valid with key k1", verdict)
	}
	if gotPath != ProposalVerifyRoute {
		t.Errorf("path %q, want %q", gotPath, ProposalVerifyRoute)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ") || strings.Count(gotAuth, ".") != 2 {
		t.Errorf("Authorization %q is not a signed token", gotAuth)
	}
	if string(gotBody["expected_robot_id"]) != `"7"` || !strings.Contains(string(gotBody["proposal"]), `"signature"`) {
		t.Errorf("body %v does not carry the proposal and the robot", gotBody)
	}
}

func TestProposalVerify_AnInvalidProposalIsAVerdictAndAnythingElseIsUnavailable(t *testing.T) {
	status := http.StatusOK
	body := `{"valid":false,"reason":"bad-signature","key_id":"k1"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	v := newTestVerifier(t, srv.URL)
	verdict, err := v.Verify(context.Background(), json.RawMessage(`{}`), "1")
	if err != nil || verdict.Valid || verdict.Reason != "bad-signature" {
		t.Fatalf("verdict %+v err %v, want an invalid verdict", verdict, err)
	}
	status, body = http.StatusBadRequest, `{"error":"x"}`
	verdict, err = v.Verify(context.Background(), json.RawMessage(`{}`), "1")
	if err != nil || verdict.Valid {
		t.Errorf("400: verdict %+v err %v, want a refusal that is a verdict", verdict, err)
	}
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable, http.StatusInternalServerError} {
		status, body = code, `{}`
		_, err = v.Verify(context.Background(), json.RawMessage(`{}`), "1")
		if !errors.Is(err, ErrProposalUnavailable) {
			t.Errorf("HTTP %d: err %v, want ErrProposalUnavailable", code, err)
		}
	}
	status, body = http.StatusOK, `not json`
	if _, err = v.Verify(context.Background(), json.RawMessage(`{}`), "1"); !errors.Is(err, ErrProposalUnavailable) {
		t.Errorf("unreadable answer: err %v, want ErrProposalUnavailable", err)
	}
}

func TestProposalVerify_AServerThatIsDownOrRedirectsIsUnavailable(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the redirect was followed to %s", r.URL)
	}))
	defer elsewhere.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	v := newTestVerifier(t, srv.URL)
	if _, err := v.Verify(context.Background(), json.RawMessage(`{}`), "1"); !errors.Is(err, ErrProposalUnavailable) {
		t.Errorf("redirect: err %v, want ErrProposalUnavailable", err)
	}
	srv.Close()
	if _, err := v.Verify(context.Background(), json.RawMessage(`{}`), "1"); !errors.Is(err, ErrProposalUnavailable) {
		t.Errorf("server down: err %v, want ErrProposalUnavailable", err)
	}
}

func TestProposalVerify_ATokenFileIsReadForEveryRequest(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"valid":true,"reason":"ok"}`))
	}))
	defer srv.Close()
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("tok-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := NewProposalVerifier(ProposalVerifyConfig{Enabled: true, ServerURL: srv.URL, TokenFile: tokenPath, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), json.RawMessage(`{}`), "1"); err != nil || gotAuth != "Bearer tok-1" {
		t.Fatalf("auth %q err %v, want Bearer tok-1", gotAuth, err)
	}
	if err := os.WriteFile(tokenPath, []byte("tok-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), json.RawMessage(`{}`), "1"); err != nil || gotAuth != "Bearer tok-2" {
		t.Fatalf("auth %q err %v, want Bearer tok-2", gotAuth, err)
	}
	if err := os.Remove(tokenPath); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), json.RawMessage(`{}`), "1"); !errors.Is(err, ErrProposalUnavailable) {
		t.Errorf("no token file: err %v, want ErrProposalUnavailable", err)
	}
}

func TestProposalVerify_TheSettingIsChecked(t *testing.T) {
	key := proposalKeyFile(t)
	good := ProposalVerifyConfig{Enabled: true, ServerURL: "https://server.example", IdentityKeyFile: key, Timeout: time.Second}
	if err := good.Validate(); err != nil {
		t.Fatalf("a good setting: %v", err)
	}
	if err := (ProposalVerifyConfig{}).Validate(); err != nil {
		t.Errorf("off must validate: %v", err)
	}
	cases := map[string]func(c *ProposalVerifyConfig){
		"no address":             func(c *ProposalVerifyConfig) { c.ServerURL = "" },
		"credentials in the url": func(c *ProposalVerifyConfig) { c.ServerURL = "https://u:p@server.example" },
		"plain http elsewhere":   func(c *ProposalVerifyConfig) { c.ServerURL = "http://server.example:3000" },
		"no credential":          func(c *ProposalVerifyConfig) { c.IdentityKeyFile = "" },
		"two credentials":        func(c *ProposalVerifyConfig) { c.TokenFile = "/run/secrets/token" },
		"no timeout":             func(c *ProposalVerifyConfig) { c.Timeout = 0 },
		"a long timeout":         func(c *ProposalVerifyConfig) { c.Timeout = 2 * time.Minute },
	}
	for name, mutate := range cases {
		c := good
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	plain := good
	plain.ServerURL = "http://server.example:3000"
	plain.AllowPlainHTTP = true
	if err := plain.Validate(); err != nil {
		t.Errorf("allow_plain_http: %v", err)
	}
	local := good
	local.ServerURL = "http://127.0.0.1:3000"
	if err := local.Validate(); err != nil {
		t.Errorf("this machine over http: %v", err)
	}
	if _, err := NewProposalVerifier(ProposalVerifyConfig{Enabled: true, ServerURL: "https://server.example", IdentityKeyFile: filepath.Join(t.TempDir(), "missing"), Timeout: time.Second}); err == nil {
		t.Error("a missing key file was accepted")
	}
}

func TestProposalVerify_TheSectionOfTheConfigurationFile(t *testing.T) {
	off, err := ParseConfig([]byte(`{}`))
	if err != nil || off.ProposalVerifySetting().Enabled {
		t.Fatalf("empty file: %v %+v", err, off.ProposalVerifySetting())
	}
	on, err := ParseConfig([]byte(`{"proposal_verify":{"enabled":true,"server_url":"https://server.example","token_file":"/run/secrets/t","timeout_seconds":3,"require":true}}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	s := on.ProposalVerifySetting()
	if !s.Enabled || !s.Require || s.Timeout != 3*time.Second || s.TokenFile != "/run/secrets/t" {
		t.Errorf("setting %+v", s)
	}
	if _, err := ParseConfig([]byte(`{"proposal_verify":{"enabled":true}}`)); err == nil {
		t.Error("an enabled section without an address was accepted")
	}
	if _, err := ParseConfig([]byte(`{"proposal_verify":{"enabled":false,"surprise":1}}`)); err == nil {
		t.Error("an unknown key was accepted")
	}
}
