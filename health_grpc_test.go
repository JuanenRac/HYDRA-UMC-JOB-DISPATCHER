// HYDRA-UMC-JOB-DISPATCHER - health_grpc_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package main

import (
	"strings"
	"testing"
	"time"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

func noEnv(string) string { return "" }

func TestHealthGRPCIsOffUnlessEnabled(t *testing.T) {
	for _, section := range []*dispatcher.HealthGRPCSection{nil, {}, {Enabled: false, Listen: "0.0.0.0:1"}} {
		cfg, err := healthGRPCSettings(section, "", noEnv, nil)
		if cfg != nil || err != nil {
			t.Errorf("a section that is off must change nothing: %v %v", cfg, err)
		}
	}
}

func TestHealthGRPCDefaultsToLoopbackWithoutBorrowingTheAPIToken(t *testing.T) {
	cfg, err := healthGRPCSettings(&dispatcher.HealthGRPCSection{Enabled: true}, "api-token", noEnv, nil)
	if err != nil || cfg == nil {
		t.Fatalf("%v %v", cfg, err)
	}
	if cfg.Listen != "127.0.0.1:50090" || cfg.Token != "" {
		t.Errorf("loopback with no token named must stay open to this machine: %+v", cfg)
	}
}

func TestHealthGRPCNetworkAddressNeedsAToken(t *testing.T) {
	section := &dispatcher.HealthGRPCSection{Enabled: true, Listen: "0.0.0.0:50090"}
	if _, err := healthGRPCSettings(section, "", noEnv, nil); err == nil {
		t.Fatal("an address reachable from the network without a token must be refused")
	}
	cfg, err := healthGRPCSettings(section, "api-token", noEnv, nil)
	if err != nil || cfg.Token != "api-token" {
		t.Errorf("the token of the API is the fallback on the network: %+v %v", cfg, err)
	}
	section.TokenEnv = "HEALTH_TOKEN"
	cfg, err = healthGRPCSettings(section, "api-token", func(name string) string {
		if name == "HEALTH_TOKEN" {
			return "own-token"
		}
		return ""
	}, nil)
	if err != nil || cfg.Token != "own-token" {
		t.Errorf("token_env wins: %+v %v", cfg, err)
	}
	if _, err := healthGRPCSettings(section, "api-token", noEnv, nil); err == nil || !strings.Contains(err.Error(), "HEALTH_TOKEN") {
		t.Errorf("a token_env that is empty must be an error naming the variable: %v", err)
	}
	if _, err := healthGRPCSettings(&dispatcher.HealthGRPCSection{Enabled: true, Listen: "nonsense"}, "t", noEnv, nil); err == nil {
		t.Error("an address that is not host:port must be refused")
	}
}

func TestHealthGRPCValuesCarryOver(t *testing.T) {
	conns, watchers, interval, keepalive := 3, 2, 2.5, 10.0
	cfg, err := healthGRPCSettings(&dispatcher.HealthGRPCSection{Enabled: true, MaxConnections: &conns, MaxWatchers: &watchers, WatchIntervalSeconds: &interval, KeepaliveSeconds: &keepalive}, "", noEnv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConnections != 3 || cfg.MaxWatchers != 2 || cfg.WatchInterval != 2500*time.Millisecond || cfg.Keepalive != 10*time.Second {
		t.Errorf("%+v", cfg)
	}
}

func TestHealthGRPCSectionInTheConfigurationFile(t *testing.T) {
	good := `{"health_grpc":{"enabled":true,"listen":"127.0.0.1:50090","max_watchers":4,"watch_interval_seconds":2}}`
	cfg, err := dispatcher.ParseConfig([]byte(good))
	if err != nil || cfg.HealthGRPC == nil || !cfg.HealthGRPC.Enabled {
		t.Fatalf("%v %v", cfg, err)
	}
	for name, bad := range map[string]string{
		"unknown key":        `{"health_grpc":{"enabeld":true}}`,
		"watchers too many":  `{"health_grpc":{"enabled":true,"max_watchers":5000}}`,
		"connections zero":   `{"health_grpc":{"enabled":true,"max_connections":0}}`,
		"interval too small": `{"health_grpc":{"enabled":true,"watch_interval_seconds":0}}`,
		"keepalive too low":  `{"health_grpc":{"enabled":true,"keepalive_seconds":0.5}}`,
	} {
		if _, err := dispatcher.ParseConfig([]byte(bad)); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	// a section that is off is not checked: nothing of it applies
	if _, err := dispatcher.ParseConfig([]byte(`{"health_grpc":{"enabled":false,"max_watchers":5000}}`)); err != nil {
		t.Errorf("a section that is off is not validated: %v", err)
	}
}

// With the identity key of the family the signed token of the server opens the service next to its own token, and nothing is open any more.
func TestHealthGRPCAcceptsTheIdentityOfTheServer(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef-test")
	cfg, err := healthGRPCSettings(&dispatcher.HealthGRPCSection{Enabled: true, Listen: "0.0.0.0:7001"}, "", noEnv, key)
	if err != nil || cfg == nil || cfg.Identity == nil {
		t.Fatalf("an address reachable from the network needs no token of its own when the identity is on: %+v %v", cfg, err)
	}
	if _, err := healthGRPCSettings(&dispatcher.HealthGRPCSection{Enabled: true, Listen: "0.0.0.0:7001"}, "", noEnv, nil); err == nil {
		t.Error("without the identity key and without a token that address is still refused")
	}
	now := time.Now()
	if !cfg.Identity(dispatcher.SignIdentityToken(key, "hydra-umc-server", "viewer", now)) {
		t.Error("a viewer token of the server is accepted: the service only reads")
	}
	for name, token := range map[string]string{
		"none":    "",
		"garbage": "not-a-token",
		"foreign": dispatcher.SignIdentityToken([]byte("another-key-of-more-than-32-characters!"), "x", "viewer", now),
		"expired": dispatcher.SignIdentityToken(key, "x", "viewer", now.Add(-time.Hour)),
	} {
		if cfg.Identity(token) {
			t.Errorf("%s token must be refused", name)
		}
	}
}
