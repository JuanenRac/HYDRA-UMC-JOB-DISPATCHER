// HYDRA-UMC-JOB-DISPATCHER - main_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// What the process refuses to start with: an API that can register robots and submit and cancel
// jobs must not listen on the network without a token.
package main

import "testing"

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
