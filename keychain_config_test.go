package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeConfigKeychain struct {
	data              map[string][]byte
	readErr, writeErr error
	corrupt           bool
}

func (k *fakeConfigKeychain) Read(service string) ([]byte, bool, error) {
	if k.readErr != nil {
		return nil, false, k.readErr
	}
	b, ok := k.data[service]
	return append([]byte(nil), b...), ok, nil
}
func (k *fakeConfigKeychain) Write(service string, data []byte) error {
	if k.writeErr != nil {
		return k.writeErr
	}
	if k.data == nil {
		k.data = map[string][]byte{}
	}
	if k.corrupt {
		k.data[service] = []byte(`{}`)
	} else {
		k.data[service] = append([]byte(nil), data...)
	}
	return nil
}

func TestMigrateConfigToKeychain(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "")
	path := filepath.Join(t.TempDir(), "linear.json")
	original := []byte(`{"endpoint":"https://example.com/mcp","oauth":{"access_token":"fake-access","refresh_token":"fake-refresh"}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	store := &fakeConfigKeychain{}
	cfg, err := loadKeychainConfig(path, "linear", store)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "https://example.com/mcp" || cfg.OAuth == nil || cfg.OAuth.AccessToken != "fake-access" {
		t.Fatal("migration changed configuration")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy file still exists: %v", err)
	}
	if !bytes.Equal(store.data["linear"], original) {
		t.Fatal("keychain data differs from original")
	}
	loaded, err := loadKeychainConfig(path, "linear", store)
	if err != nil || loaded.OAuth.RefreshToken != "fake-refresh" {
		t.Fatalf("reload: %v", err)
	}
}

func TestMigrationPreservesLegacyOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store *fakeConfigKeychain
	}{
		{"write failure", &fakeConfigKeychain{writeErr: errors.New("denied")}},
		{"verification failure", &fakeConfigKeychain{corrupt: true}},
		{"conflicting keychain", &fakeConfigKeychain{data: map[string][]byte{"notion": []byte(`{"scope":"other"}`)}}},
		{"keychain read failure", &fakeConfigKeychain{readErr: errors.New("denied")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "notion.json")
			original := []byte(`{"oauth":{"access_token":"fake"}}`)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadKeychainConfig(path, "notion", tc.store); err == nil {
				t.Fatal("expected migration error")
			}
			b, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(b, original) {
				t.Fatalf("legacy config changed: %v", err)
			}
		})
	}
}

func TestMigrationResumesAfterFileRemovalFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notion.json")
	legacy := []byte(`{"scope":"read"}`)
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	store := &fakeConfigKeychain{data: map[string][]byte{"notion": []byte("{ \"scope\": \"read\" }")}}
	if _, err := loadKeychainConfig(path, "notion", store); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy file still exists: %v", err)
	}
}

func TestEnvKeyDoesNotPersist(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "environment-secret")
	store := &fakeConfigKeychain{data: map[string][]byte{"linear": []byte(`{"api_key":"stored-secret"}`)}}
	cfg, err := loadKeychainConfig(filepath.Join(t.TempDir(), "linear.json"), "linear", store)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "environment-secret" {
		t.Fatal("environment key did not take precedence")
	}
	cfg.OAuth = &oauthState{AccessToken: "new-session"}
	if err := saveKeychainConfig("linear", cfg, store); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(store.data["linear"]), "environment-secret") {
		t.Fatal("environment key persisted")
	}
	var saved config
	if err := json.Unmarshal(store.data["linear"], &saved); err != nil || saved.APIKey != "stored-secret" || saved.OAuth.AccessToken != "new-session" {
		t.Fatalf("stored config incorrect: %v", err)
	}
}
