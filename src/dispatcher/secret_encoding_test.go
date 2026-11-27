// HYDRA-UMC-JOB-DISPATCHER - dispatcher/secret_encoding_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
package dispatcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretFilesMayCarryAByteOrderMarkButNoOtherEncoding(t *testing.T) {
	secret := strings.Repeat("k", MinIdentityKeyLength)
	path := filepath.Join(t.TempDir(), "secret")
	write := func(data []byte) {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	verifier := &ProposalVerifier{cfg: ProposalVerifyConfig{TokenFile: path}}

	write([]byte("\ufeff" + secret + "\r\n"))
	if got, err := LoadIdentityKey(path); err != nil || string(got) != secret {
		t.Fatalf("identity key with a mark: %q, %v", got, err)
	}
	if got, err := verifier.credential(); err != nil || got != secret {
		t.Fatalf("token with a mark: %q, %v", got, err)
	}

	write([]byte{0xFF, 0xFE, 'k', 0, 'k', 0})
	if _, err := verifier.credential(); err == nil {
		t.Fatal("a UTF-16 token file was accepted")
	}
	write(append([]byte{0xFF, 0xFE}, []byte(strings.Repeat("k\x00", MinIdentityKeyLength))...))
	if _, err := LoadIdentityKey(path); err == nil {
		t.Fatal("a UTF-16 key file was accepted")
	}
}
