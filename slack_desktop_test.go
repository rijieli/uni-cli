package main

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/syndtr/goleveldb/leveldb"
)

type keychainExitError int

func (e keychainExitError) Error() string { return "security failed" }
func (e keychainExitError) ExitCode() int { return int(e) }

func TestRemoveDesktopSession(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantRemove bool
		wantErr    bool
	}{
		{"removed", nil, true, false},
		{"already absent", keychainExitError(44), false, false},
		{"keychain failure", keychainExitError(1), false, true},
		{"command failure", errors.New("unavailable"), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			removed, err := removeDesktopSession(func() error { return tc.err })
			if removed != tc.wantRemove || (err != nil) != tc.wantErr {
				t.Fatalf("removed=%v, err=%v", removed, err)
			}
		})
	}
}

func TestOpenSlackStorageWaitsForLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("desktop sessions are supported on macOS")
	}
	path := filepath.Join(t.TempDir(), "leveldb")
	locked, err := leveldb.OpenFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		closed <- locked.Close()
	}()
	var output bytes.Buffer
	db, err := openSlackStorage(path, &output)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Waiting for Slack") {
		t.Fatalf("missing waiting message: %q", output.String())
	}
}

func TestOpenSlackStorageDoesNotRetryOtherErrors(t *testing.T) {
	var output bytes.Buffer
	_, err := openSlackStorage(filepath.Join(t.TempDir(), "missing"), &output)
	if err == nil || output.Len() != 0 {
		t.Fatalf("error=%v output=%q", err, output.String())
	}
}

func TestReadSlackCookieRowRecoversPrivateSnapshot(t *testing.T) {
	if _, err := os.Stat("/usr/bin/sqlite3"); err != nil {
		t.Skip("sqlite3 is not available")
	}
	path := filepath.Join(t.TempDir(), "Cookies")
	setup := `CREATE TABLE meta(key TEXT, value TEXT); INSERT INTO meta VALUES('version','23');
CREATE TABLE cookies(host_key TEXT, name TEXT, encrypted_value BLOB, value TEXT, padding TEXT);
INSERT INTO cookies VALUES('.slack.com','d',x'','before','');
WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<300)
INSERT INTO cookies SELECT '.example.com','other',x'','','' FROM n;`
	if out, err := exec.Command("/usr/bin/sqlite3", path, setup).CombinedOutput(); err != nil {
		t.Fatalf("create fixture: %v: %s", err, out)
	}
	cmd := exec.Command("/usr/bin/sqlite3", "-interactive", path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	_, err = io.WriteString(stdin, "PRAGMA cache_size=5; PRAGMA cache_spill=ON; BEGIN IMMEDIATE; UPDATE cookies SET padding=printf('%04000d',0), value='after'; SELECT 'ready';\n")
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	ready := false
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "ready" {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatalf("fixture not ready: %v", scanner.Err())
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	beforeDB, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeJournal, err := os.ReadFile(path + "-journal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("/usr/bin/sqlite3", "-readonly", path, slackCookieQuery).Output(); err == nil {
		t.Fatal("fixture did not reproduce a hot journal")
	}
	row, err := readSlackCookieRow(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(row), "6265666F7265") {
		t.Fatalf("recovered wrong row: %q", row)
	}
	afterDB, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterJournal, err := os.ReadFile(path + "-journal")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeDB, afterDB) || !bytes.Equal(beforeJournal, afterJournal) {
		t.Fatal("original database was modified")
	}
}

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
	if got := findWorkspaceTokens(utf8)["demo"]; got != "xoxc-fake" {
		t.Fatal("UTF-8 token not found")
	}
	if got := findWorkspaceTokens(utf8)["missing"]; got != "" {
		t.Fatal("unexpected token")
	}
	encoded := []byte{0}
	for _, w := range utf16.Encode([]rune(doc)) {
		v := make([]byte, 2)
		binary.LittleEndian.PutUint16(v, w)
		encoded = append(encoded, v...)
	}
	if got := findWorkspaceTokens(encoded)["demo"]; got != "xoxc-fake" {
		t.Fatal("UTF-16 token not found")
	}
}

func TestSelectSlackWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		tokens                 map[string]string
		requested, input, want string
		wantErr                bool
	}{
		{"single", map[string]string{"demo": "token"}, "", "", "demo", false},
		{"selection", map[string]string{"demo": "token", "other": "token"}, "", "2\n", "other", false},
		{"explicit", map[string]string{"demo": "token", "other": "token"}, "demo", "", "demo", false},
		{"invalid selection", map[string]string{"demo": "token", "other": "token"}, "", "3\n", "", true},
		{"no workspaces", nil, "", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectSlackWorkspace(tc.tokens, tc.requested, strings.NewReader(tc.input), io.Discard)
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("got %q, error %v", got, err)
			}
		})
	}
}

func TestLoginPromptsShareInput(t *testing.T) {
	in := bufio.NewReader(strings.NewReader("y\n2\n"))
	running := true
	if err := closeSlackForLogin(in, io.Discard, func() bool { return running }, func() error { running = false; return nil }); err != nil {
		t.Fatal(err)
	}
	workspace, err := selectSlackWorkspace(map[string]string{"first": "token", "second": "token"}, "", in, io.Discard)
	if err != nil || workspace != "second" {
		t.Fatalf("workspace=%q err=%v", workspace, err)
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
	var output bytes.Buffer
	if err := desktopAPI(client, &output, desktopSession{Token: "xoxc-fake", Cookie: "xoxd-fake"}, "conversations.replies", url.Values{"channel": {"C1"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"messages": []`) {
		t.Fatalf("missing output: %q", output.String())
	}
	if err := desktopAPI(client, io.Discard, desktopSession{}, "chat.postMessage", nil); err == nil {
		t.Fatal("accepted unsupported write method")
	}
}

func TestDesktopExpiredSessionGuidance(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":false,"error":"invalid_auth"}`)), Header: make(http.Header)}, nil
	})}
	err := desktopAPI(client, io.Discard, desktopSession{Token: "xoxc-fake", Cookie: "xoxd-fake"}, "search.messages", nil)
	if err == nil || !strings.Contains(err.Error(), "uni-cli slack login") {
		t.Fatalf("missing login guidance: %v", err)
	}
}
