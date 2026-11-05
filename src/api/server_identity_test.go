// HYDRA-UMC-JOB-DISPATCHER - src/api/server_identity_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The identity of HYDRA-UMC-SERVER over real HTTP round-trips: off by default; on, a signed token of the server is accepted and nothing is open.
package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

var identityTestKey = []byte("0123456789abcdef0123456789abcdef")

func signedFor(role string) string {
	// SignIdentityToken always signs the role it is given; the user name is a fixed one.
	return dispatcher.SignIdentityToken(identityTestKey, "ana", role, time.Now())
}

func TestIdentity_OffByDefaultNothingChanges(t *testing.T) {
	s := New(dispatcher.NewEngine())
	if rec := withToken(t, s, http.MethodGet, "/jobs", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("GET /jobs without any credential = %d, want 200 while the identity is off", rec.Code)
	}
	s.RequireToken("s3cret-token")
	if rec := withToken(t, s, http.MethodGet, "/jobs", signedFor("admin"), nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a token of the SERVER means nothing while the identity is off, got %d", rec.Code)
	}
}

func TestIdentity_OnNothingIsOpenAndAViewerOnlyReads(t *testing.T) {
	s := New(dispatcher.NewEngine())
	s.SetIdentityKey(identityTestKey)
	for _, path := range []string{"/jobs", "/robots", "/health"} {
		if rec := withToken(t, s, http.MethodGet, path, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a credential = %d, want 401 (not even /health is open)", path, rec.Code)
		}
		if rec := withToken(t, s, http.MethodGet, path, "not-a-token", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with a bad token = %d, want 401", path, rec.Code)
		}
	}
	for _, path := range []string{"/jobs", "/robots", "/health"} {
		if rec := withToken(t, s, http.MethodGet, path, signedFor("viewer"), nil); rec.Code != http.StatusOK {
			t.Errorf("GET %s as a viewer = %d, want 200", path, rec.Code)
		}
	}
	if rec := withToken(t, s, http.MethodPost, "/robots", signedFor("viewer"), []byte("{}")); rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /robots as a viewer = %d, want 401", rec.Code)
	}
	for _, role := range []string{"operator", "admin"} {
		if rec := withToken(t, s, http.MethodPost, "/robots", signedFor(role), []byte("{}")); rec.Code == http.StatusUnauthorized {
			t.Errorf("POST /robots as %s was refused as unauthorized", role)
		}
	}
	forged := dispatcher.SignIdentityToken([]byte("another-key-of-more-than-32-characters!"), "ana", "admin", time.Now())
	if rec := withToken(t, s, http.MethodGet, "/jobs", forged, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("a token signed with another key = %d, want 401", rec.Code)
	}
}

func TestIdentity_TheTokenOfTheAPIKeepsWorkingAndMetricsFollow(t *testing.T) {
	s := New(dispatcher.NewEngine())
	s.SetMetricsEnabled(true)
	s.RequireToken("s3cret-token")
	s.SetIdentityKey(identityTestKey)
	if rec := withToken(t, s, http.MethodPost, "/robots", "s3cret-token", []byte("{}")); rec.Code == http.StatusUnauthorized {
		t.Errorf("the token of the API was refused with the identity on")
	}
	if rec := metricsFrom(s, "127.0.0.1:5000", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /metrics from this machine without a credential = %d, want 401 with the identity on", rec.Code)
	}
	if rec := metricsFrom(s, "127.0.0.1:5000", signedFor("viewer")); rec.Code != http.StatusOK {
		t.Errorf("GET /metrics as a viewer = %d, want 200", rec.Code)
	}
}
