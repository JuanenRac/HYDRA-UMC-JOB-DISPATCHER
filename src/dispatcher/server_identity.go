// HYDRA-UMC-JOB-DISPATCHER - the signed token of HYDRA-UMC-SERVER as the identity of a caller
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// HYDRA-UMC-SERVER is the one issuer of identity of the family. A service written in Go verifies the token that the server issues at login: an HS256 JWT with
// the claims username, role (admin, operator or viewer) and exp. It is a switch and it is off: nothing is asked unless the operator gives the signing key in a
// file. The checks are those of the Python services (tools/canonical/server_identity.py of HYDRA-UMC-SDK): the algorithm is exactly HS256, the signature is
// compared in constant time, exp is present, finite and in the future, nbf has passed, iat is not in the future, the user name is not empty and the role is known.
package dispatcher

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
)

const (
	maxIdentityTokenChars = 4096
	identityIssuedSkew    = 5 * time.Second
)

// ServerIdentity is who a valid token of the SERVER says is calling.
type ServerIdentity struct {
	Username string
	Role     string
}

// CanWrite says whether the role may change something: a viewer only reads.
func (i ServerIdentity) CanWrite() bool { return i.Role == "admin" || i.Role == "operator" }

var errMalformedToken = errors.New("the token is malformed")

func decodeTokenPart(segment string) (map[string]any, error) {
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(segment, "="))
	if err != nil {
		return nil, errMalformedToken
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil || value == nil {
		return nil, errMalformedToken
	}
	return value, nil
}

// finiteSeconds reads a claim that must be a finite number of seconds.
func finiteSeconds(claims map[string]any, name string) (float64, bool, bool) {
	raw, present := claims[name]
	if !present {
		return 0, false, true
	}
	number, ok := raw.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, true, false
	}
	return number, true, true
}

// VerifyServerToken returns the identity in a signed token of the SERVER, or an error that says why there is none. The error names no key and no token.
func VerifyServerToken(token string, key []byte, now time.Time) (ServerIdentity, error) {
	if token == "" {
		return ServerIdentity{}, errors.New("a signed token of HYDRA-UMC-SERVER is required")
	}
	if len(token) > maxIdentityTokenChars {
		return ServerIdentity{}, errMalformedToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return ServerIdentity{}, errMalformedToken
	}
	header, err := decodeTokenPart(parts[0])
	if err != nil {
		return ServerIdentity{}, err
	}
	if alg, _ := header["alg"].(string); alg != "HS256" {
		return ServerIdentity{}, errors.New("the token algorithm is not accepted (only HS256)")
	}
	signature, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[2], "="))
	if err != nil {
		return ServerIdentity{}, errMalformedToken
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(mac.Sum(nil), signature) {
		return ServerIdentity{}, errors.New("the token signature is not valid")
	}
	claims, err := decodeTokenPart(parts[1])
	if err != nil {
		return ServerIdentity{}, err
	}
	moment := float64(now.UnixNano()) / 1e9
	exp, present, ok := finiteSeconds(claims, "exp")
	if !present || !ok {
		return ServerIdentity{}, errors.New("the token has no expiry")
	}
	if exp <= moment {
		return ServerIdentity{}, errors.New("the token has expired")
	}
	if nbf, present, ok := finiteSeconds(claims, "nbf"); present && (!ok || nbf > moment) {
		return ServerIdentity{}, errors.New("the token is not valid yet")
	}
	if iat, present, ok := finiteSeconds(claims, "iat"); present && (!ok || iat > moment+identityIssuedSkew.Seconds()) {
		return ServerIdentity{}, errors.New("the token was issued in the future")
	}
	username, _ := claims["username"].(string)
	if username == "" {
		return ServerIdentity{}, errors.New("the token names no user")
	}
	role, _ := claims["role"].(string)
	if role != "admin" && role != "operator" && role != "viewer" {
		return ServerIdentity{}, errors.New("the token carries no known role")
	}
	return ServerIdentity{Username: username, Role: role}, nil
}
