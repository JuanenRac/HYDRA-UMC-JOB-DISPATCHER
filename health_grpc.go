// HYDRA-UMC-JOB-DISPATCHER - health_grpc.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// Turns the "health_grpc" section of the configuration file into the settings of the gRPC health
// service (src/healthgrpc). Nothing is served unless the section is enabled.
package main

import (
	"fmt"
	"time"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/healthgrpc"
)

// healthGRPCSettings returns the settings of the gRPC health service, or nil while it is off. The
// token is the one named by token_env, or else, for an address other than loopback, the token of the HTTP API. An address other than
// loopback without any token is refused, and so is a token_env that names an empty variable. With the identity key of the family (identityKey, the key of
// HYDRA-UMC-SERVER) the signed token of the server is accepted too, next to the token above, and nothing is open any more: a call with no credential is refused
// even on loopback and an address reachable from the network needs no token of its own.
func healthGRPCSettings(section *dispatcher.HealthGRPCSection, apiToken string, getenv func(string) string, identityKey []byte) (*healthgrpc.Config, error) {
	if section == nil || !section.Enabled {
		return nil, nil
	}
	cfg := healthgrpc.Config{Listen: section.Listen}
	if cfg.Listen == "" {
		cfg.Listen = healthgrpc.DefaultListen
	}
	// The token of the HTTP API is a fallback only for an address reachable from the network: on loopback, with no token named,
	// the service is open to this machine (the watchdog of the cell dials it without credentials).
	if healthgrpc.ListenProblem(cfg.Listen, "x") == "" && healthgrpc.ListenProblem(cfg.Listen, "") != "" {
		cfg.Token = apiToken
	}
	if section.TokenEnv != "" {
		token := getenv(section.TokenEnv)
		if token == "" {
			return nil, fmt.Errorf("health_grpc.token_env names %s, which is empty or not set", section.TokenEnv)
		}
		cfg.Token = token
	}
	if identityKey != nil {
		cfg.Identity = func(token string) bool {
			_, err := dispatcher.VerifyServerToken(token, identityKey, time.Now())
			return err == nil
		}
	}
	if problem := healthgrpc.ListenProblem(cfg.Listen, cfg.Token); problem != "" && cfg.Identity == nil {
		return nil, fmt.Errorf("%s", problem)
	}
	if section.MaxConnections != nil {
		cfg.MaxConnections = *section.MaxConnections
	}
	if section.MaxWatchers != nil {
		cfg.MaxWatchers = *section.MaxWatchers
	}
	if section.WatchIntervalSeconds != nil {
		cfg.WatchInterval = time.Duration(*section.WatchIntervalSeconds * float64(time.Second))
	}
	if section.KeepaliveSeconds != nil {
		cfg.Keepalive = time.Duration(*section.KeepaliveSeconds * float64(time.Second))
	}
	return &cfg, nil
}
