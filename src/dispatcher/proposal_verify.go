// HYDRA-UMC-JOB-DISPATCHER - the check of a signed trajectory proposal by HYDRA-UMC-SERVER
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// A trajectory that the trajectory engine proposes can carry an Ed25519 signature. The dispatcher holds no key to check it: it asks HYDRA-UMC-SERVER
// (POST /api/trajectory/proposals/verify), which keeps the public keys of the operators. The answer is a fact about the proposal, never an order. The check
// fails closed: a server that cannot be reached, that refuses the credential or that has the check off is "could not be checked", never "valid".
//
// The credential is the identity the SERVER issues: a short token signed with its signing key (the same file the production events use) or a token read from
// a file. Nothing secret is logged or returned. The credential only goes over HTTPS or to this machine; a redirect is never followed.
package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ProposalVerifyRoute is the route of HYDRA-UMC-SERVER that checks a signed proposal.
const ProposalVerifyRoute = "/api/trajectory/proposals/verify"

// maxProposalAnswerBytes is the most that is read from the answer of the server.
const maxProposalAnswerBytes = 64 * 1024

// ErrProposalUnavailable says that the proposal could not be checked (the server is away, refuses, or has the check off). It is not a verdict.
var ErrProposalUnavailable = errors.New("the signed proposal could not be checked")

// ProposalVerifyConfig is the setting of the "proposal_verify" section.
type ProposalVerifyConfig struct {
	Enabled bool
	// ServerURL is the address of HYDRA-UMC-SERVER (the route is added to it).
	ServerURL string
	// IdentityKeyFile holds the signing key of the SERVER; a token is signed with it for every request. TokenFile holds a token instead.
	IdentityKeyFile string
	TokenFile       string
	Timeout         time.Duration
	// Require refuses a job that arrives without a proposal.
	Require bool
	// AllowPlainHTTP lets the credential go over plain HTTP to a machine other than this one (a bench).
	AllowPlainHTTP bool
}

// ProposalVerdict is what the server says of a proposal.
type ProposalVerdict struct {
	Valid  bool
	Reason string
	KeyID  string
}

// ProposalVerifier asks the server whether a signed proposal is valid.
type ProposalVerifier struct {
	cfg    ProposalVerifyConfig
	key    []byte
	client *http.Client
	now    func() time.Time
}

// Validate refuses a setting that cannot work. Off, anything goes.
func (c ProposalVerifyConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(c.ServerURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return errors.New("proposal_verify.server_url must be an http(s) address without credentials")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) && !c.AllowPlainHTTP {
		return errors.New("proposal_verify.server_url must be https (or this machine): the credential does not go over plain HTTP, unless allow_plain_http is true")
	}
	if (c.IdentityKeyFile == "") == (c.TokenFile == "") {
		return errors.New("proposal_verify needs exactly one of identity_key_file and token_file")
	}
	if c.Timeout <= 0 || c.Timeout > time.Minute {
		return errors.New("proposal_verify.timeout_seconds must be greater than 0 and at most 60")
	}
	return nil
}

// NewProposalVerifier checks the setting and reads the identity key, when there is one.
func NewProposalVerifier(cfg ProposalVerifyConfig) (*ProposalVerifier, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	v := &ProposalVerifier{cfg: cfg, now: time.Now}
	if cfg.IdentityKeyFile != "" {
		key, err := LoadIdentityKey(cfg.IdentityKeyFile)
		if err != nil {
			return nil, fmt.Errorf("proposal_verify.identity_key_file: %w", err)
		}
		v.key = key
	}
	v.client = &http.Client{
		Timeout: cfg.Timeout,
		// a redirect would carry the credential to wherever the answer points
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return v, nil
}

// Require says whether a job without a proposal is refused.
func (v *ProposalVerifier) Require() bool { return v.cfg.Require }

func (v *ProposalVerifier) credential() (string, error) {
	if v.key != nil {
		return SignIdentityToken(v.key, "job-dispatcher", "operator", v.now()), nil
	}
	raw, err := os.ReadFile(v.cfg.TokenFile)
	if err != nil {
		return "", errors.New("cannot read the token file")
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", errors.New("the token file is empty")
	}
	return token, nil
}

// Verify asks the server about one proposal (the JSON object the trajectory engine signed) for the robot that is to run it. A verdict comes back only
// when the server answered; every other case is an error that wraps ErrProposalUnavailable.
func (v *ProposalVerifier) Verify(ctx context.Context, proposal json.RawMessage, expectedRobotID string) (ProposalVerdict, error) {
	unavailable := func(why string) (ProposalVerdict, error) {
		return ProposalVerdict{}, fmt.Errorf("%w: %s", ErrProposalUnavailable, why)
	}
	token, err := v.credential()
	if err != nil {
		return unavailable(err.Error())
	}
	body, err := json.Marshal(map[string]any{"proposal": proposal, "expected_robot_id": expectedRobotID})
	if err != nil {
		return unavailable("the proposal is not JSON")
	}
	endpoint := strings.TrimRight(strings.TrimSpace(v.cfg.ServerURL), "/") + ProposalVerifyRoute
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return unavailable("the address of the server is not usable")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := v.client.Do(req)
	if err != nil {
		return unavailable("the server cannot be reached")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxProposalAnswerBytes))
	switch resp.StatusCode {
	case http.StatusOK:
		var answer struct {
			Valid  bool   `json:"valid"`
			Reason string `json:"reason"`
			KeyID  string `json:"key_id"`
		}
		if err := json.Unmarshal(raw, &answer); err != nil {
			return unavailable("the answer of the server is not readable")
		}
		return ProposalVerdict{Valid: answer.Valid, Reason: answer.Reason, KeyID: answer.KeyID}, nil
	case http.StatusBadRequest:
		return ProposalVerdict{Valid: false, Reason: "the server does not take this proposal"}, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return unavailable("the server refused the credential")
	case http.StatusNotFound:
		return unavailable("the server has the check off (HYDRA_UMC_PROPOSAL_VERIFY)")
	case http.StatusServiceUnavailable:
		return unavailable("the server holds no key of any operator")
	default:
		return unavailable(fmt.Sprintf("the server answered HTTP %d", resp.StatusCode))
	}
}
