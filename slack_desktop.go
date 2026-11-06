package main

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/spf13/cobra"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

const desktopKeychainService = "uni-cli Slack desktop session"

type desktopSession struct {
	Workspace string `json:"workspace"`
	Token     string `json:"token"`
	Cookie    string `json:"cookie"`
}

func slackDesktopCmd(c *client) *cobra.Command {
	cmd := &cobra.Command{Use: "slack", Short: "Access Slack through a local desktop session (macOS)", Long: `Read and search Slack using a workspace signed in to the Slack desktop app on this Mac.

Start with "uni-cli slack login". The session is stored in macOS Keychain; it has the same Slack access as your signed-in account. Run "uni-cli slack logout" to remove the saved copy.`}
	cmd.AddCommand(&cobra.Command{Use: "login [workspace]", Short: "Connect a signed-in Slack desktop workspace", Long: `Connect a workspace signed in to the Slack desktop app on this Mac.

If Slack is running, you can choose whether to close it. When more than one workspace is signed in, choose from the list. Optionally provide the workspace subdomain, for example "miidii" for miidii.slack.com.

Login reads Slack's local session and may request macOS Keychain access. The selected session is saved in Keychain and has the same Slack access as your account. You can reopen Slack after login.`, Example: "  uni-cli slack login\n  uni-cli slack login miidii", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS != "darwin" {
			return errors.New("Slack desktop login is supported on macOS only")
		}
		input := bufio.NewReader(cmd.InOrStdin())
		fmt.Fprintln(cmd.OutOrStdout(), "Stores your Slack session in Keychain; macOS may ask for access.")
		running := func() bool { return exec.Command("/usr/bin/pgrep", "-x", "Slack").Run() == nil }
		if err := closeSlackForLogin(input, cmd.OutOrStdout(), running, func() error {
			return exec.Command("/usr/bin/pkill", "-TERM", "-x", "Slack").Run()
		}); err != nil {
			return err
		}
		workspace := ""
		if len(args) > 0 {
			workspace = args[0]
		}
		s, err := importDesktopSession(workspace, input, cmd.OutOrStdout())
		if err != nil {
			return err
		}
		if err := storeDesktopSession(s); err != nil {
			return err
		}
		cmd.Printf("Connected to %s.\n", s.Workspace)
		return nil
	}})
	cmd.AddCommand(&cobra.Command{Use: "logout", Short: "Remove the saved Slack session", Long: "Remove the saved Slack session from macOS Keychain. This does not sign you out of Slack or revoke the original session.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if runtime.GOOS != "darwin" {
			return errors.New("Slack desktop sessions are supported on macOS only")
		}
		removed, err := removeDesktopSession(func() error {
			return exec.Command("/usr/bin/security", "delete-generic-password", "-s", desktopKeychainService, "-a", "uni-cli").Run()
		})
		if err != nil {
			return err
		}
		if !removed {
			cmd.Println("No Slack session to log out.")
			return nil
		}
		cmd.Println("Slack session removed.")
		return nil
	}})
	cmd.AddCommand(&cobra.Command{Use: "status", Short: "Show the connected Slack workspace", Long: "Show the workspace stored in macOS Keychain. This does not check whether the Slack session is still valid.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := loadDesktopSession()
		if err != nil {
			return err
		}
		cmd.Printf("Workspace: %s\n", s.Workspace)
		return nil
	}})
	cmd.AddCommand(&cobra.Command{Use: "read <message-url>", Short: "Read a Slack message and its thread", Long: "Read a Slack message and up to 100 messages in its thread. Copy the message link from Slack; it must belong to the connected workspace. Results are printed as JSON.", Example: "  uni-cli slack read 'https://workspace.slack.com/archives/C123/p1234567890123456'", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		workspace, channel, ts, err := parseSlackMessageURL(args[0])
		if err != nil {
			return err
		}
		s, err := loadDesktopSession()
		if err != nil {
			return err
		}
		if !strings.EqualFold(workspace, s.Workspace) {
			return fmt.Errorf("message belongs to workspace %q; connected workspace is %q (run: uni-cli slack login %s)", workspace, s.Workspace, workspace)
		}
		return desktopAPI(c.http, cmd.OutOrStdout(), s, "conversations.replies", url.Values{"channel": {channel}, "ts": {ts}, "limit": {"100"}})
	}})
	cmd.AddCommand(&cobra.Command{Use: "search <query>", Short: "Search messages in the connected workspace", Long: "Search messages using Slack search syntax. Returns up to 20 results as JSON.", Example: "  uni-cli slack search 'in:general roadmap'", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, err := loadDesktopSession()
		if err != nil {
			return err
		}
		return desktopAPI(c.http, cmd.OutOrStdout(), s, "search.messages", url.Values{"query": {args[0]}, "count": {"20"}})
	}})
	return cmd
}

func removeDesktopSession(remove func() error) (bool, error) {
	err := remove()
	if err == nil {
		return true, nil
	}
	var exitError interface{ ExitCode() int }
	if errors.As(err, &exitError) && exitError.ExitCode() == 44 {
		return false, nil // macOS security: errSecItemNotFound
	}
	return false, fmt.Errorf("could not remove Slack session from Keychain: %w", err)
}

func closeSlackForLogin(in io.Reader, out io.Writer, running func() bool, stop func() error) error {
	if !running() {
		return nil
	}
	fmt.Fprint(out, "Close Slack to continue? [y/N] ")
	answer, _ := bufio.NewReader(in).ReadString('\n')
	if !strings.EqualFold(strings.TrimSpace(answer), "y") {
		return errors.New("login cancelled; Slack is still running")
	}
	if err := stop(); err != nil {
		if !running() {
			return nil
		}
		return fmt.Errorf("could not close Slack: %w", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for running() {
		if time.Now().After(deadline) {
			return errors.New("Slack did not close; quit it manually and retry")
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

func parseSlackMessageURL(raw string) (workspace, channel, ts string, err error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return "", "", "", errors.New("expected an HTTPS Slack message URL")
	}
	host := strings.ToLower(u.Hostname())
	workspace, ok := strings.CutSuffix(host, ".slack.com")
	if !ok || workspace == "" || strings.Contains(workspace, ".") {
		return "", "", "", errors.New("expected a workspace.slack.com URL")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "archives" || len(parts[1]) < 2 || !strings.HasPrefix(parts[2], "p") || len(parts[2]) != 17 {
		return "", "", "", errors.New("expected a Slack message permalink")
	}
	for _, ch := range parts[1] {
		if !(ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9') {
			return "", "", "", errors.New("invalid channel ID")
		}
	}
	for _, ch := range parts[2][1:] {
		if ch < '0' || ch > '9' {
			return "", "", "", errors.New("invalid message timestamp")
		}
	}
	return workspace, parts[1], parts[2][1:11] + "." + parts[2][11:], nil
}

func desktopAPI(base *http.Client, out io.Writer, s desktopSession, method string, form url.Values) error {
	if method != "conversations.replies" && method != "search.messages" {
		return errors.New("unsupported Slack operation")
	}
	if !strings.HasPrefix(s.Token, "xoxc-") || !strings.HasPrefix(s.Cookie, "xoxd-") {
		return errors.New("invalid desktop session; reconnect")
	}
	req, err := http.NewRequest(http.MethodPost, "https://slack.com/api/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "d", Value: s.Cookie})
	hc := *base
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Slack returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	var status struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err = json.Unmarshal(b, &status); err != nil {
		return err
	}
	if !status.OK {
		if status.Error == "invalid_auth" || status.Error == "account_inactive" || status.Error == "token_revoked" || status.Error == "token_expired" {
			return fmt.Errorf("Slack: %s; sign in to the Slack app and run: uni-cli slack login", status.Error)
		}
		return fmt.Errorf("Slack: %s", status.Error)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, b, "", "  "); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, pretty.String())
	return err
}

func storeDesktopSession(s desktopSession) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	value := base64.StdEncoding.EncodeToString(b)
	cmd := exec.Command("/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader("add-generic-password -U -s \"" + desktopKeychainService + "\" -a uni-cli -w " + value + "\n")
	if err := cmd.Run(); err != nil {
		return errors.New("could not save desktop session in macOS Keychain")
	}
	check, err := exec.Command("/usr/bin/security", "find-generic-password", "-s", desktopKeychainService, "-a", "uni-cli", "-w").Output()
	if err != nil || strings.TrimSpace(string(check)) != value {
		return errors.New("could not verify desktop session in macOS Keychain")
	}
	return nil
}

func loadDesktopSession() (desktopSession, error) {
	var s desktopSession
	if runtime.GOOS != "darwin" {
		return s, errors.New("Slack desktop sessions are supported on macOS only")
	}
	b, err := exec.Command("/usr/bin/security", "find-generic-password", "-s", desktopKeychainService, "-a", "uni-cli", "-w").Output()
	if err != nil {
		return s, errors.New("could not read the Slack session from macOS Keychain; allow access or run: uni-cli slack login")
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return s, errors.New("invalid desktop session in Keychain")
	}
	if err = json.Unmarshal(decoded, &s); err != nil {
		return s, errors.New("invalid desktop session in Keychain")
	}
	return s, nil
}

func importDesktopSession(workspace string, in io.Reader, out io.Writer) (desktopSession, error) {
	var s desktopSession
	workspace = strings.ToLower(strings.TrimSpace(workspace))
	if strings.ContainsAny(workspace, "./:") {
		return s, errors.New("provide the workspace subdomain, e.g. miidii")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return s, err
	}
	roots := []string{filepath.Join(home, "Library/Application Support/Slack"), filepath.Join(home, "Library/Containers/com.tinyspeck.slackmacgap/Data/Library/Application Support/Slack")}
	var root string
	for _, p := range roots {
		if info, e := os.Stat(filepath.Join(p, "Local Storage/leveldb")); e == nil && info.IsDir() {
			root = p
			break
		}
	}
	if root == "" {
		return s, errors.New("Slack desktop storage not found; install Slack and sign in to a workspace first")
	}
	db, err := openSlackStorage(filepath.Join(root, "Local Storage/leveldb"), out)
	if err != nil {
		return s, err
	}
	it := db.NewIterator(nil, nil)
	tokens := map[string]string{}
	for it.Next() {
		if !bytes.Contains(it.Key(), []byte("localConfig_v2")) {
			continue
		}
		for name, token := range findWorkspaceTokens(it.Value()) {
			tokens[name] = token
		}
	}
	iterErr := it.Error()
	it.Release()
	db.Close()
	if iterErr != nil {
		return s, iterErr
	}
	workspace, err = selectSlackWorkspace(tokens, workspace, in, out)
	if err != nil {
		return s, err
	}
	cookie, err := readSlackCookie(filepath.Join(root, "Cookies"))
	if err != nil {
		return s, err
	}
	return desktopSession{Workspace: workspace, Token: tokens[workspace], Cookie: cookie}, nil
}

func openSlackStorage(path string, out io.Writer) (*leveldb.DB, error) {
	deadline := time.Now().Add(10 * time.Second)
	announced := false
	for {
		db, err := leveldb.OpenFile(path, &opt.Options{ReadOnly: true})
		if err == nil {
			return db, nil
		}
		if !errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("open Slack storage: %w", err)
		}
		if time.Now().After(deadline) {
			return nil, errors.New("Slack's local database is still in use; make sure Slack is fully closed, then run: uni-cli slack login")
		}
		if !announced {
			fmt.Fprintln(out, "Waiting for Slack to close...")
			announced = true
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func findWorkspaceTokens(raw []byte) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == 0 && len(raw[1:])%2 == 0 {
		v := raw[1:]
		words := make([]uint16, len(v)/2)
		for i := range words {
			words[i] = binary.LittleEndian.Uint16(v[2*i:])
		}
		raw = []byte(string(utf16.Decode(words)))
	} else if raw[0] == 1 {
		raw = raw[1:]
	}
	var doc struct {
		Teams map[string]struct {
			URL   string `json:"url"`
			Token string `json:"token"`
		} `json:"teams"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	tokens := make(map[string]string)
	for _, team := range doc.Teams {
		u, e := url.Parse(team.URL)
		if e == nil && u.Scheme == "https" && strings.HasPrefix(team.Token, "xoxc-") {
			name, ok := strings.CutSuffix(strings.ToLower(u.Hostname()), ".slack.com")
			if ok && name != "" && !strings.Contains(name, ".") {
				tokens[name] = team.Token
			}
		}
	}
	return tokens
}

func selectSlackWorkspace(tokens map[string]string, requested string, in io.Reader, out io.Writer) (string, error) {
	if requested != "" {
		if tokens[requested] == "" {
			return "", fmt.Errorf("workspace %q not found in Slack desktop storage", requested)
		}
		return requested, nil
	}
	names := make([]string, 0, len(tokens))
	for name := range tokens {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", errors.New("no signed-in workspaces found; sign in to the Slack desktop app and retry")
	}
	if len(names) == 1 {
		return names[0], nil
	}
	fmt.Fprintln(out, "Choose a Slack workspace:")
	for i, name := range names {
		fmt.Fprintf(out, "  %d. %s\n", i+1, name)
	}
	fmt.Fprint(out, "Workspace number: ")
	answer, _ := bufio.NewReader(in).ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(answer))
	if err != nil || n < 1 || n > len(names) {
		return "", errors.New("invalid workspace selection; run login again")
	}
	return names[n-1], nil
}

func readSlackCookie(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", errors.New("Slack cookie database not found")
	}
	out, err := readSlackCookieRow(path)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.TrimSpace(string(out)), "|")
	if len(parts) != 4 {
		return "", errors.New("Slack session cookie not found")
	}
	host, err := hex.DecodeString(parts[0])
	if err != nil {
		return "", err
	}
	encrypted, err := hex.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	plain, err := hex.DecodeString(parts[2])
	if err != nil {
		return "", err
	}
	if len(plain) > 0 {
		if strings.HasPrefix(string(plain), "xoxd-") {
			return string(plain), nil
		}
		return "", errors.New("invalid Slack session cookie")
	}
	var key []byte
	for _, account := range []string{"Slack Key", "Slack App Store Key"} {
		b, e := exec.Command("/usr/bin/security", "find-generic-password", "-s", "Slack Safe Storage", "-a", account, "-w").Output()
		if e == nil {
			key = bytes.TrimRight(b, "\r\n")
			break
		}
	}
	if len(key) == 0 {
		return "", errors.New("could not access Slack Safe Storage in Keychain")
	}
	version, err := strconv.Atoi(parts[3])
	if err != nil {
		return "", errors.New("invalid Slack cookie database version")
	}
	return decryptSlackCookie(encrypted, string(key), host, version)
}

const slackCookieQuery = `SELECT hex(host_key),hex(encrypted_value),hex(value),(SELECT value FROM meta WHERE key='version') FROM cookies WHERE name='d' AND (host_key='slack.com' OR host_key='.slack.com') ORDER BY length(encrypted_value) DESC LIMIT 1;`

func readSlackCookieRow(path string) ([]byte, error) {
	out, err := exec.Command("/usr/bin/sqlite3", "-readonly", "-separator", "|", path, slackCookieQuery).Output()
	if err == nil {
		return out, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || !strings.Contains(string(exitErr.Stderr), "attempt to write a readonly database") {
		return nil, fmt.Errorf("could not read Slack cookie database: %w", err)
	}

	// A hot journal needs recovery. Recover a private snapshot rather than
	// modifying Slack's database. SQLite may create sidecar files in this directory.
	dir, err := os.MkdirTemp("", "uni-cli-slack-cookies-")
	if err != nil {
		return nil, fmt.Errorf("create temporary cookie database: %w", err)
	}
	defer os.RemoveAll(dir)
	snapshot := filepath.Join(dir, "Cookies")
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		if err := copyCookieSnapshot(path+suffix, snapshot+suffix, suffix != ""); err != nil {
			return nil, err
		}
	}
	out, err = exec.Command("/usr/bin/sqlite3", "-separator", "|", snapshot, slackCookieQuery).Output()
	if err != nil {
		return nil, fmt.Errorf("could not recover temporary Slack cookie database: %w", err)
	}
	return out, nil
}

func copyCookieSnapshot(src, dst string, optional bool) error {
	in, err := os.Open(src)
	if errors.Is(err, os.ErrNotExist) && optional {
		return nil
	}
	if err != nil {
		return fmt.Errorf("copy Slack cookie database: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary cookie database: %w", err)
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return fmt.Errorf("copy Slack cookie database: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close temporary cookie database: %w", closeErr)
	}
	return nil
}

func decryptSlackCookie(encrypted []byte, password string, host []byte, version int) (string, error) {
	if len(encrypted) < 3 || (string(encrypted[:3]) != "v10" && string(encrypted[:3]) != "v11") || (len(encrypted)-3) == 0 || (len(encrypted)-3)%aes.BlockSize != 0 {
		return "", errors.New("unsupported Slack cookie format")
	}
	key, err := pbkdf2.Key(sha1.New, password, []byte("saltysalt"), 1003, 16)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	buf := make([]byte, len(encrypted)-3)
	cipher.NewCBCDecrypter(block, bytes.Repeat([]byte(" "), aes.BlockSize)).CryptBlocks(buf, encrypted[3:])
	pad := int(buf[len(buf)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(buf) {
		return "", errors.New("could not decrypt Slack cookie")
	}
	for _, b := range buf[len(buf)-pad:] {
		if int(b) != pad {
			return "", errors.New("could not decrypt Slack cookie")
		}
	}
	buf = buf[:len(buf)-pad]
	if version >= 24 {
		hash := sha256.Sum256(host)
		if len(buf) < len(hash) || !bytes.Equal(buf[:len(hash)], hash[:]) {
			return "", errors.New("Slack cookie domain verification failed")
		}
		buf = buf[len(hash):]
	}
	if !strings.HasPrefix(string(buf), "xoxd-") {
		return "", errors.New("invalid Slack session cookie")
	}
	return string(buf), nil
}
