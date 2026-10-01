package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func signed(t *testing.T, version string, content []byte) (Release, Keyring) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	release := Release{Version: version, SHA256: hex.EncodeToString(sum[:])}
	message, err := manifest(release)
	if err != nil {
		t.Fatal(err)
	}
	release.Signatures = []Signature{
		{KeyID: "someone-else", Signature: "AAAA"},
		{KeyID: "test", Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, []byte(message)))},
	}
	return release, Keyring{"test": public}
}

func TestManifestIsWhatTheAPISigns(t *testing.T) {
	got, err := manifest(Release{Version: "1.4.2", SHA256: strings.Repeat("ab", 32)})
	want := `{"channel":"PRINT_AGENT","format":"quicktable-release/1","minNativeVersion":null,"sha256":"` +
		strings.Repeat("ab", 32) + `","version":"1.4.2"}`
	if err != nil || got != want {
		t.Fatalf("got %s, %v", got, err)
	}
}

func TestVerify(t *testing.T) {
	release, keys := signed(t, "1.2.0", []byte("the agent"))

	if err := Verify(release, keys); err != nil {
		t.Fatalf("a genuine release: %v", err)
	}

	otherVersion := release
	otherVersion.Version = "9.9.9"
	if Verify(otherVersion, keys) == nil {
		t.Error("a signature must not carry over to another version")
	}
	otherBytes := release
	otherBytes.SHA256 = strings.Repeat("0", 64)
	if Verify(otherBytes, keys) == nil {
		t.Error("a signature must not carry over to other bytes")
	}
	if Verify(release, Keyring{}) == nil {
		t.Error("a key the agent doesn't trust must not count")
	}
	stranger, _, _ := ed25519.GenerateKey(nil)
	if Verify(release, Keyring{"test": stranger}) == nil {
		t.Error("another key under the same id must not count")
	}
	malformed := release
	malformed.Version = `1.0","x":"`
	if Verify(malformed, keys) == nil {
		t.Error("a malformed version must be rejected")
	}
}

func TestTrustedKeysIncludesTheProductionKey(t *testing.T) {
	if key, ok := TrustedKeys()["qt-2026-09"]; !ok || len(key) != ed25519.PublicKeySize {
		t.Fatal("the production key is missing")
	}
}

func TestDownloadChecksTheBytes(t *testing.T) {
	content := []byte("MZ the new agent")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agent.exe" {
			_, _ = w.Write(content)
			return
		}
		_, _ = w.Write([]byte("something else"))
	}))
	defer server.Close()
	release, _ := signed(t, "1.2.0", content)
	exe := filepath.Join(t.TempDir(), "agent.exe")

	release.URL = server.URL + "/agent.exe"
	path, err := Download(context.Background(), release, exe)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(content) || path != exe+".new" {
		t.Fatalf("downloaded %q to %s", got, path)
	}

	release.URL = server.URL + "/tampered.exe"
	if _, err := Download(context.Background(), release, exe); err == nil {
		t.Fatal("a file that doesn't match the release's sha256 must be rejected")
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Error("a rejected download must not be left behind")
	}

	release.URL = "http://example.com/agent.exe"
	if _, err := Download(context.Background(), release, exe); err == nil {
		t.Fatal("plain http to another host must be rejected")
	}
}

func TestSwapAndRevert(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "agent.exe")
	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) string {
		content, _ := os.ReadFile(path)
		return string(content)
	}
	write(exe, "old")
	write(exe+".new", "new")

	if err := Swap(exe, exe+".new"); err != nil {
		t.Fatal(err)
	}
	if read(exe) != "new" || read(exe+".old") != "old" {
		t.Fatalf("after the swap: %q, old %q", read(exe), read(exe+".old"))
	}

	if err := Revert(exe); err != nil {
		t.Fatal(err)
	}
	if read(exe) != "old" {
		t.Fatalf("after going back: %q", read(exe))
	}
	for _, leftover := range []string{exe + ".old", exe + ".new"} {
		if _, err := os.Stat(leftover); !os.IsNotExist(err) {
			t.Errorf("%s was left behind", leftover)
		}
	}
	if Revert(exe) == nil {
		t.Error("there is nothing to go back to twice")
	}
}

func TestInWindow(t *testing.T) {
	cases := []struct {
		minute, start, end int
		want               bool
	}{
		{200, 180, 360, true},
		{180, 180, 360, true},
		{360, 180, 360, false},
		{100, 180, 360, false},
		{30, 1380, 120, true}, // 23:00–02:00, at 00:30
		{1400, 1380, 120, true},
		{600, 1380, 120, false},
		{600, 300, 300, false},
	}
	for _, c := range cases {
		if got := InWindow(c.minute, c.start, c.end); got != c.want {
			t.Errorf("InWindow(%d, %d, %d) = %v", c.minute, c.start, c.end, got)
		}
	}
}

// A signature made by the API's own signing code (quicktable-api's
// releaseSigning.ts) with a throwaway key: the two sides must agree on the
// signed text, byte for byte.
func TestVerifyAcceptsWhatTheAPISigns(t *testing.T) {
	public, _ := hex.DecodeString("1d8a019965902189bae7cc7dbdaa65a74fe5de11d7f6758a1e28195817ac037d")
	release := Release{
		Version: "1.4.2-beta.1",
		SHA256:  strings.Repeat("ab", 32),
		Signatures: []Signature{{
			KeyID:     "vector",
			Signature: "T4DKXiOWWlMCEPhoOoLmMfSaS1gowpzW4mQJRTAzNxFrEPnrdbDDtSpiqFjC6bOrAb0Y/iOH4P2pypAOFF8zBg==",
		}},
	}

	if err := Verify(release, Keyring{"vector": ed25519.PublicKey(public)}); err != nil {
		t.Fatal(err)
	}
}
