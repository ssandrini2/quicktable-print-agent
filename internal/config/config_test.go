package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	store := Store{Dir: filepath.Join(t.TempDir(), "nested")}

	empty, err := store.Load()
	if err != nil || empty.PairingToken() != "" {
		t.Fatalf("a missing file is an empty config, got %+v, %v", empty, err)
	}

	config := Config{APIURL: "https://api.example.test"}
	if err := config.SetPairingToken("secret-token"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(config); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.APIURL != config.APIURL || loaded.PairingToken() != "secret-token" {
		t.Fatalf("got %+v", loaded)
	}
}

func TestTokenIsNotStoredInTheClear(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	var config Config
	if err := config.SetPairingToken("secret-token"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(config); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(store.Dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-token") {
		t.Fatal("the token is readable in the config file")
	}
}

func TestAnUnreadableTokenMeansUnpaired(t *testing.T) {
	config := Config{Token: "not base64 !!"}
	if config.PairingToken() != "" {
		t.Fatal("expected no token")
	}

	if err := config.SetPairingToken(""); err != nil || config.Token != "" {
		t.Fatalf("clearing the token left %q, %v", config.Token, err)
	}
}
