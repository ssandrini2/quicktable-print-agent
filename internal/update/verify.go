// Package update is how the agent replaces itself with a newer release:
// check it is a genuine QuickTable release, download it, swap the executable,
// and go back to the old one if the new one doesn't start.
package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Release is what the API says the agent should run (see the heartbeat).
type Release struct {
	Version    string      `json:"version"`
	URL        string      `json:"url"`
	SHA256     string      `json:"sha256"`
	Signatures []Signature `json:"signatures"`
}

// Signature is one Ed25519 signature of the release's manifest.
type Signature struct {
	KeyID     string `json:"keyId"`
	Signature string `json:"signature"`
}

// The keys releases are signed with, by id: raw Ed25519 public keys in hex.
// The same keyring the tablets carry — see quicktable-api/docs/release-signing.md.
var trustedKeys = map[string]string{
	"qt-2026-09": "020cc8cfc87b70fe8b290598758766736a1ae258a55e5c5e7fa0b4567bf5d6a4",
}

// extraKeys adds keys at build time, for builds that update from an API with
// its own signing key (a local one): "keyId:hex,keyId:hex".
// Set with -ldflags "-X github.com/quicktable/print-agent/internal/update.extraKeys=…".
var extraKeys = ""

var (
	versionShape = regexp.MustCompile(`^\d+(\.\d+){0,3}(-[0-9A-Za-z][0-9A-Za-z.-]*)?$`)
	sha256Shape  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Keyring is the public keys the agent trusts, by key id.
type Keyring map[string]ed25519.PublicKey

// TrustedKeys is the keyring compiled into this build.
func TrustedKeys() Keyring {
	keys := Keyring{}
	add := func(id, hexKey string) {
		raw, err := hex.DecodeString(strings.TrimSpace(hexKey))
		if err == nil && len(raw) == ed25519.PublicKeySize {
			keys[strings.TrimSpace(id)] = ed25519.PublicKey(raw)
		}
	}
	for id, hexKey := range trustedKeys {
		add(id, hexKey)
	}
	for _, entry := range strings.Split(extraKeys, ",") {
		if id, hexKey, ok := strings.Cut(entry, ":"); ok {
			add(id, hexKey)
		}
	}
	return keys
}

// manifest is the exact text the API signs for a print agent release: keys
// in alphabetical order, no spaces. It is rebuilt here from the fields —
// never taken from the API — after checking each has the expected shape, so
// nothing needs escaping.
func manifest(release Release) (string, error) {
	if !versionShape.MatchString(release.Version) {
		return "", fmt.Errorf("malformed version %q", release.Version)
	}
	if !sha256Shape.MatchString(release.SHA256) {
		return "", errors.New("malformed sha256")
	}
	return fmt.Sprintf(
		`{"channel":"PRINT_AGENT","format":"quicktable-release/1","minNativeVersion":null,"sha256":"%s","version":"%s"}`,
		release.SHA256, release.Version,
	), nil
}

// Verify checks the release is signed by a key this build trusts. The
// signature covers the version and the sha256 of the file, so a genuine old
// build can't be passed off as another version, nor different bytes as this one.
func Verify(release Release, keys Keyring) error {
	message, err := manifest(release)
	if err != nil {
		return err
	}
	// Every trusted key is tried against every signature, whatever id it
	// came with: the id is a label, the key is what counts.
	for _, signature := range release.Signatures {
		raw, err := base64.StdEncoding.DecodeString(signature.Signature)
		if err != nil {
			continue
		}
		for _, key := range keys {
			if ed25519.Verify(key, []byte(message), raw) {
				return nil
			}
		}
	}
	return errors.New("the release isn't signed by a key this agent trusts")
}
