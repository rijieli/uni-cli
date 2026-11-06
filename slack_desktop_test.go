package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestCloseSlackForLogin(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		running     bool
		wantStop    bool
		wantErr     bool
	}{
		{"already closed", "", false, false, false},
		{"confirmed", "y\n", true, true, false},
		{"declined", "n\n", true, false, true},
		{"empty input", "", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			running, stopped := tc.running, false
			var output bytes.Buffer
			err := closeSlackForLogin(strings.NewReader(tc.input), &output, func() bool { return running }, func() error {
				stopped, running = true, false
				return nil
			})
			if stopped != tc.wantStop || (err != nil) != tc.wantErr {
				t.Fatalf("stopped=%v err=%v", stopped, err)
			}
		})
	}
}

func TestDesktopMessageURL(t *testing.T) {
	workspace, channel, ts, err := parseSlackMessageURL("https://miidii.slack.com/archives/C06948M9L6L/p1793946896200869")
	if err != nil || workspace != "miidii" || channel != "C06948M9L6L" || ts != "1793946896.200869" {
		t.Fatalf("bad parse: %s %s %s %v", workspace, channel, ts, err)
	}
	for _, input := range []string{"https://miidii.slack.com.evil.test/archives/C1/p1793946896200869", "http://miidii.slack.com/archives/C1/p1793946896200869", "https://miidii.slack.com/archives/C1/pinvalid"} {
		if _, _, _, err := parseSlackMessageURL(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}
func TestDesktopWorkspaceToken(t *testing.T) {
	doc := `{"teams":{"one":{"url":"https://demo.slack.com/","token":"xoxc-fake"},"two":{"url":"https://other.slack.com/","token":"xoxc-other"}}}`
	utf8 := append([]byte{1}, []byte(doc)...)
	if got := findWorkspaceToken(utf8, "demo"); got != "xoxc-fake" {
		t.Fatal("UTF-8 token not found")
	}
	if got := findWorkspaceToken(utf8, "missing"); got != "" {
		t.Fatal("unexpected token")
	}
	encoded := []byte{0}
	for _, w := range utf16.Encode([]rune(doc)) {
		v := make([]byte, 2)
		binary.LittleEndian.PutUint16(v, w)
		encoded = append(encoded, v...)
	}
	if got := findWorkspaceToken(encoded, "demo"); got != "xoxc-fake" {
		t.Fatal("UTF-16 token not found")
	}
}
func TestDesktopCookieDecryption(t *testing.T) {
	host := []byte(".slack.com")
	h := sha256.Sum256(host)
	plain := append(h[:], []byte("xoxd-fake-cookie")...)
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(padding)}, padding)...)
	key, err := pbkdf2.Key(sha1.New, "synthetic", []byte("saltysalt"), 1003, 16)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	encrypted := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, bytes.Repeat([]byte(" "), aes.BlockSize)).CryptBlocks(encrypted, plain)
	encrypted = append([]byte("v10"), encrypted...)
	got, err := decryptSlackCookie(encrypted, "synthetic", host, 24)
	if err != nil || got != "xoxd-fake-cookie" {
		t.Fatalf("decrypt failed: %v", err)
	}
	if _, err := decryptSlackCookie(encrypted, "synthetic", []byte(".other.com"), 24); err == nil {
		t.Fatal("accepted mismatched cookie domain")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDesktopRequest(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://slack.com/api/conversations.replies" || r.Header.Get("Authorization") != "Bearer xoxc-fake" {
			t.Error("incorrect destination or authentication")
		}
		cookie, err := r.Cookie("d")
		if err != nil || cookie.Value != "xoxd-fake" {
			t.Error("incorrect session cookie")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"messages":[]}`)), Header: make(http.Header)}, nil
	})}
	if err := desktopAPI(client, desktopSession{Token: "xoxc-fake", Cookie: "xoxd-fake"}, "conversations.replies", url.Values{"channel": {"C1"}}); err != nil {
		t.Fatal(err)
	}
	if err := desktopAPI(client, desktopSession{}, "chat.postMessage", nil); err == nil {
		t.Fatal("accepted unsupported write method")
	}
}
