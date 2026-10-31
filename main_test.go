// HYDRA-UMC-JOB-DISPATCHER - main_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// What the process refuses to start with: an API that can register robots and submit and cancel
// jobs must not listen on the network without a token.
package main

import (
	"testing"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

func TestIsLoopbackAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8090": true, "localhost:8090": true, "LOCALHOST:8090": true, "[::1]:8090": true,
		":8090": false, "0.0.0.0:8090": false, "192.168.0.180:8090": false, "example.com:8090": false, "garbage": false,
	} {
		if got := isLoopbackAddr(addr); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestStartupProblem(t *testing.T) {
	if p := startupProblem("127.0.0.1:8090", "", false); p != "" {
		t.Errorf("loopback without a token must start, got %q", p)
	}
	if p := startupProblem(":8090", "", false); p == "" {
		t.Error("every interface without a token must be refused (this was the default)")
	}
	if p := startupProblem("192.168.0.180:8090", "", false); p == "" {
		t.Error("a LAN address without a token must be refused")
	}
	if p := startupProblem(":8090", "a-token", false); p != "" {
		t.Errorf("a token makes the network address acceptable, got %q", p)
	}
	if p := startupProblem(":8090", "", true); p != "" {
		t.Errorf("-allow-anonymous says it on purpose, got %q", p)
	}
}

func TestBuildToolCatalog(t *testing.T) {
	if c, err := buildToolCatalog("", " "); c != nil || err != nil {
		t.Fatalf("no tools given means no catalog, got %v, %v", c, err)
	}
	c, err := buildToolCatalog("", "PnP, Laser,,")
	if err != nil || len(c.Names()) != 2 {
		t.Fatalf("list: %v, %v", c, err)
	}
	if _, err := buildToolCatalog("does-not-exist.json", ""); err == nil {
		t.Fatal("a missing catalog file must stop the start")
	}
}

func TestProductionEventsProblem(t *testing.T) {
	for _, c := range []struct {
		on        bool
		file, url string
		refused   bool
	}{
		{false, "", "", false},
		{false, "events.jsonl", "", false},
		{true, "", "", true},
		{true, "events.jsonl", "", false},
		{true, "", "http://127.0.0.1:1/", false},
		{true, "events.jsonl", "http://127.0.0.1:1/", false},
	} {
		if got := productionEventsProblem(c.on, c.file, c.url) != ""; got != c.refused {
			t.Errorf("productionEventsProblem(%v, %q, %q) refused = %v, want %v", c.on, c.file, c.url, got, c.refused)
		}
	}
}

func TestConfigProblem(t *testing.T) {
	withRange, err := dispatcher.ParseConfig([]byte(`{"eta_interval":{"enabled":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p := configProblem(withRange, true); p != "" {
		t.Errorf("a range with the estimates on must start, got %q", p)
	}
	if p := configProblem(withRange, false); p == "" {
		t.Error("a range without the estimates must be refused")
	}
	if p := configProblem(dispatcher.FileConfig{}, false); p != "" {
		t.Errorf("an empty configuration must start, got %q", p)
	}
}
