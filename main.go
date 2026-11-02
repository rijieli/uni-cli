package main

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

//go:embed workflows
var workflows embed.FS

const protocol = "2025-03-26"

// preset holds a known service's defaults; any other service needs "endpoint" in its config.
type preset struct {
	endpoint  string
	apiKeyEnv string // env var overriding api_key; empty: oauth only
	readScope string // scope requested by login --read; empty: unsupported
	shortcut  shortcut
}

// shortcut is a service command calling one tool with a one-line-per-item listing.
type shortcut struct {
	use, short, tool string
	query            bool // a non-object argument is sent as {"query": arg}
}

var presets = map[string]preset{
	"linear": {endpoint: "https://mcp.linear.app/mcp", apiKeyEnv: "LINEAR_API_KEY", readScope: "read",
		shortcut: shortcut{"issues [json-args]", "Call list_issues; one line per issue", "list_issues", false}},
	"notion": {endpoint: "https://mcp.notion.com/mcp",
		shortcut: shortcut{"search <query | json-args>", "Call notion-search; one line per result", "notion-search", true}},
}

type config struct {
	Endpoint string      `json:"endpoint"`
	APIKey   string      `json:"api_key,omitempty"`
	Scope    string      `json:"scope,omitempty"`
	OAuth    *oauthState `json:"oauth,omitempty"`
}

type oauthState struct {
	AuthorizationEndpoint string    `json:"authorization_endpoint,omitempty"`
	TokenEndpoint         string    `json:"token_endpoint,omitempty"`
	RegistrationEndpoint  string    `json:"registration_endpoint,omitempty"`
	ClientID              string    `json:"client_id,omitempty"`
	ClientSecret          string    `json:"client_secret,omitempty"`
	RedirectURI           string    `json:"redirect_uri,omitempty"`
	Resource              string    `json:"resource,omitempty"`
	Scope                 string    `json:"scope,omitempty"`
	AccessToken           string    `json:"access_token,omitempty"`
	RefreshToken          string    `json:"refresh_token,omitempty"`
	ExpiresAt             time.Time `json:"expires_at,omitempty"`
}

type asMeta struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
}

type lastCall struct {
	Tool      string          `json:"tool"`
	Args      json.RawMessage `json:"args,omitempty"`
	UpdatedAt string          `json:"updated_at"`
	Result    json.RawMessage `json:"result"`
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int   `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

type client struct {
	svc   string
	cfg   *config
	path  string
	http  *http.Client
	sid   string
	proto string
	ready bool
	next  int
}

func main() {
	if bi, ok := debug.ReadBuildInfo(); ok && version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = bi.Main.Version // go install module@vX.Y.Z
	}
	c := &client{http: &http.Client{}}
	dir := "."
	if exe, err := os.Executable(); err == nil {
		dir = filepath.Dir(exe)
	}
	root := &cobra.Command{
		Use:     "uni-cli <service> <command>",
		Short:   "Minimal MCP client for Linear, Notion, or any remote MCP server",
		Version: version,
		Long: `uni-cli - minimal MCP client

services: the presets (linear, notion) plus every <name>.json next to the binary.
A binary named after a preset (linear-cli, notion-cli) acts as "uni-cli <preset>".

config (<service>.json next to the binary; holds secrets, never results):
  {
    "endpoint": "https://mcp.example.com/mcp",   // optional for presets
    "api_key": "",            // optional; when set it wins over oauth
    "scope": "",              // optional, e.g. "read"
    "oauth": { ... }          // oauth session, when present
  }
LINEAR_API_KEY overrides api_key for linear. Results saved with --save go to <service>-last.json.
Add a custom server: write {"endpoint": "..."} to <name>.json, then: uni-cli <name> login`,
		Example: `  uni-cli linear issues '{"team": "ENG", "limit": 20}'
  uni-cli linear call save_issue '{"team": "ENG", "title": "Test"}'
  uni-cli notion search "roadmap"
  uni-cli notion call notion-fetch '{"id": "<page-url-or-id>"}'`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().DurationVar(&c.http.Timeout, "timeout", 30*time.Second, "per-request network timeout")

	names := map[string]bool{}
	for name := range presets {
		names[name] = true
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		if name := strings.TrimSuffix(filepath.Base(f), ".json"); !strings.HasSuffix(name, "-last") {
			names[name] = true
		}
	}
	for _, name := range slices.Sorted(maps.Keys(names)) {
		root.AddCommand(serviceCmd(c, name, dir))
	}
	if bin := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe"), "-cli"); presets[bin].endpoint != "" {
		root.SetArgs(append([]string{bin}, os.Args[1:]...))
	}

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func serviceCmd(c *client, name, dir string) *cobra.Command {
	svc := &cobra.Command{
		Use:   name,
		Short: "Commands for " + cmp.Or(presets[name].endpoint, name+".json"),
		PersistentPreRunE: func(*cobra.Command, []string) (err error) {
			c.svc = name
			c.path = filepath.Join(dir, name+".json")
			c.cfg, err = loadConfig(c.path, name)
			return err
		},
	}

	var readOnly, noBrowser bool
	login := &cobra.Command{
		Use:   "login",
		Short: "Authorize via OAuth in the browser",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if readOnly {
				if presets[c.svc].readScope == "" {
					return fmt.Errorf("%s has no known read-only scope; set \"scope\" in %s", c.svc, c.path)
				}
				c.cfg.Scope = presets[c.svc].readScope
			}
			return c.login(noBrowser)
		},
	}
	login.Flags().BoolVar(&readOnly, "read", false, "request read-only scope")
	login.Flags().BoolVar(&noBrowser, "no-browser", false, "print the URL without opening a browser")

	var raw, save, asJSON bool
	call := &cobra.Command{
		Use:   "call <tool> [json-args]",
		Short: "Call an MCP tool",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			return runCall(c, args[0], args[1:], raw, save, false)
		},
	}
	call.Flags().BoolVar(&raw, "raw", false, "print the raw MCP result envelope")
	call.Flags().BoolVar(&save, "save", false, "store the result in <service>-last.json (read by: last)")
	svc.AddCommand(call)
	if s := presets[name].shortcut; s.tool != "" {
		cmd := &cobra.Command{
			Use:   s.use,
			Short: s.short,
			Args:  cobra.MaximumNArgs(1),
			RunE: func(_ *cobra.Command, args []string) error {
				if s.query && len(args) == 1 && !strings.HasPrefix(strings.TrimSpace(args[0]), "{") {
					q, _ := json.Marshal(map[string]string{"query": args[0]})
					args = []string{string(q)}
				}
				return runCall(c, s.tool, args, raw, save, !asJSON)
			},
		}
		cmd.Flags().BoolVar(&raw, "raw", false, "print the raw MCP result envelope")
		cmd.Flags().BoolVar(&save, "save", false, "store the result in <service>-last.json (read by: last)")
		cmd.Flags().BoolVar(&asJSON, "json", false, "print full JSON instead of one line per item")
		svc.AddCommand(cmd)
	}

	last := &cobra.Command{
		Use:   "last",
		Short: "Reprint the last saved result (offline)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			lastPath := filepath.Join(filepath.Dir(c.path), c.svc+"-last.json")
			var lc lastCall
			b, err := os.ReadFile(lastPath)
			if err != nil || json.Unmarshal(b, &lc) != nil {
				return errors.New("no saved result in " + lastPath + " (save one with --save)")
			}
			return output(lc.Result, raw, false)
		},
	}
	last.Flags().BoolVar(&raw, "raw", false, "print the raw MCP result envelope")

	svc.AddCommand(login, last, &cobra.Command{
		Use:   "logout",
		Short: "Clear the stored OAuth session",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if c.cfg.OAuth == nil {
				fmt.Println("no oauth session")
				return nil
			}
			c.cfg.OAuth = nil
			if err := saveConfig(c.path, c.cfg); err != nil {
				return err
			}
			fmt.Println("logged out")
			return nil
		},
	}, &cobra.Command{
		Use:   "status",
		Short: "Show login state and token expiry (offline)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg := c.cfg
			if cfg.APIKey != "" {
				fmt.Println("auth:     api key (wins over oauth)")
			}
			o := cfg.OAuth
			if o == nil || o.AccessToken == "" {
				fmt.Println("oauth:    not logged in")
				return nil
			}
			fmt.Println("oauth:    logged in")
			if o.Scope != "" {
				fmt.Println("scope:   ", o.Scope)
			}
			switch d := time.Until(o.ExpiresAt).Round(time.Second); {
			case o.ExpiresAt.IsZero():
				fmt.Println("expires:  never")
			case d <= 0:
				fmt.Printf("expires:  %s (expired %s ago)\n", o.ExpiresAt.Local().Format(time.DateTime), -d)
			default:
				fmt.Printf("expires:  %s (in %s)\n", o.ExpiresAt.Local().Format(time.DateTime), d)
			}
			fmt.Println("refresh: ", map[bool]string{true: "available", false: "none (expired session requires: uni-cli " + c.svc + " login)"}[o.RefreshToken != ""])
			return nil
		},
	}, &cobra.Command{
		Use:   "tools",
		Short: "List available MCP tools",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			tools, err := c.tools()
			if err != nil {
				return err
			}
			for _, t := range tools {
				fmt.Printf("%-24s %s\n", t.Name, oneLine(t.Description))
			}
			return nil
		},
	}, &cobra.Command{
		Use:   "schema <tool>",
		Short: "Show a tool's input schema",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			tools, err := c.tools()
			if err != nil {
				return err
			}
			for _, t := range tools {
				if t.Name == args[0] {
					return printJSON(t.InputSchema)
				}
			}
			return fmt.Errorf("unknown tool %q (run: uni-cli %s tools)", args[0], c.svc)
		},
	}, &cobra.Command{
		Use:   "workflow",
		Short: "Print the service's standard workflow for coding agents (offline)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			b, err := workflows.ReadFile("workflows/" + c.svc + ".md")
			if err != nil {
				return fmt.Errorf("no built-in workflow for %s", c.svc)
			}
			fmt.Print(string(b))
			return nil
		},
	})
	return svc
}

func runCall(c *client, name string, args []string, raw, save, table bool) error {
	var arguments any
	if len(args) == 1 {
		if err := json.Unmarshal([]byte(args[0]), &arguments); err != nil {
			return fmt.Errorf("invalid json args: %w", err)
		}
	}
	if err := c.connect(); err != nil {
		return err
	}
	res, err := c.toolCall(name, arguments)
	if err != nil {
		return err
	}
	if save {
		var argRaw json.RawMessage
		if arguments != nil {
			argRaw, _ = json.Marshal(arguments)
		}
		lc, err := json.Marshal(lastCall{
			Tool:      name,
			Args:      argRaw,
			UpdatedAt: time.Now().Format(time.RFC3339),
			Result:    res,
		})
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(c.path), c.svc+"-last.json"), append(lc, '\n'), 0o600); err != nil {
			return err
		}
	}
	return output(res, raw, table)
}

func loadConfig(path, svc string) (*config, error) {
	p := presets[svc]
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg := &config{Endpoint: p.endpoint}
		if err := saveConfig(path, cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg.Endpoint = cmp.Or(cfg.Endpoint, p.endpoint)
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("%s: missing \"endpoint\"", path)
	}
	if key := os.Getenv(p.apiKeyEnv); p.apiKeyEnv != "" && key != "" {
		cfg.APIKey = key
	}
	return &cfg, nil
}

func saveConfig(path string, cfg *config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func (c *client) token() (string, error) {
	if c.cfg.APIKey != "" {
		return c.cfg.APIKey, nil
	}
	o := c.cfg.OAuth
	if o == nil || o.AccessToken == "" {
		return "", fmt.Errorf("not logged in; run: uni-cli %s login", c.svc)
	}
	if !o.ExpiresAt.IsZero() && time.Now().After(o.ExpiresAt.Add(-time.Minute)) {
		if err := c.refresh(); err != nil {
			return "", err
		}
	}
	return o.AccessToken, nil
}

func (c *client) login(noBrowser bool) error {
	md, resource, err := c.discover()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
	clientID, clientSecret, err := c.register(md.RegistrationEndpoint, redirect)
	if err != nil {
		return err
	}
	verifier := randToken(32)
	sum := sha256.Sum256([]byte(verifier))
	state := randToken(16)
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirect},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"resource":              {resource},
	}
	if c.cfg.Scope != "" {
		q.Set("scope", c.cfg.Scope)
	}
	authURL := md.AuthorizationEndpoint + "?" + q.Encode()

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		qq := r.URL.Query()
		switch {
		case qq.Get("error") != "":
			errCh <- fmt.Errorf("%s: %s", qq.Get("error"), qq.Get("error_description"))
		case qq.Get("state") != state:
			errCh <- errors.New("oauth state mismatch")
		case qq.Get("code") == "":
			errCh <- errors.New("oauth callback without code")
		default:
			codeCh <- qq.Get("code")
		}
		fmt.Fprintln(w, "uni-cli authorized. You can close this tab.")
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	fmt.Println("open this URL to authorize:")
	fmt.Println(authURL)
	if !noBrowser {
		openBrowser(authURL)
	}
	fmt.Println("waiting for authorization...")

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return err
	case <-time.After(5 * time.Minute):
		return errors.New("timed out waiting for authorization")
	}

	o := &oauthState{
		AuthorizationEndpoint: md.AuthorizationEndpoint,
		TokenEndpoint:         md.TokenEndpoint,
		RegistrationEndpoint:  md.RegistrationEndpoint,
		ClientID:              clientID,
		ClientSecret:          clientSecret,
		RedirectURI:           redirect,
		Resource:              resource,
		Scope:                 c.cfg.Scope,
	}
	if err := c.exchange(o, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirect},
		"client_id":     {clientID},
		"code_verifier": {verifier},
		"resource":      {resource},
	}); err != nil {
		return err
	}
	c.cfg.OAuth = o
	if err := saveConfig(c.path, c.cfg); err != nil {
		return err
	}
	fmt.Printf("logged in: client %s, scope %q\n", o.ClientID, o.Scope)
	return nil
}

func (c *client) refresh() error {
	o := c.cfg.OAuth
	if o == nil || o.RefreshToken == "" {
		return fmt.Errorf("not logged in; run: uni-cli %s login", c.svc)
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {o.RefreshToken},
		"client_id":     {o.ClientID},
	}
	if o.Resource != "" {
		form.Set("resource", o.Resource)
	}
	if err := c.exchange(o, form); err != nil {
		return fmt.Errorf("refresh: %w (run: uni-cli %s login)", err, c.svc)
	}
	return saveConfig(c.path, c.cfg)
}

func (c *client) exchange(o *oauthState, form url.Values) error {
	if o.ClientSecret != "" {
		form.Set("client_secret", o.ClientSecret)
	}
	resp, err := c.http.PostForm(o.TokenEndpoint, form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("token: http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(b, &tok); err != nil {
		return err
	}
	if tok.AccessToken == "" {
		return errors.New("token response without access_token")
	}
	o.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		o.RefreshToken = tok.RefreshToken
	}
	if tok.ExpiresIn > 0 {
		o.ExpiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	if tok.Scope != "" {
		o.Scope = tok.Scope
	}
	return nil
}

func (c *client) discover() (*asMeta, string, error) {
	req, err := http.NewRequest(http.MethodPost, c.cfg.Endpoint, strings.NewReader(`{}`))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()

	rmURL := attr(resp.Header.Get("WWW-Authenticate"), "resource_metadata")
	if rmURL == "" {
		u, err := url.Parse(c.cfg.Endpoint)
		if err != nil {
			return nil, "", err
		}
		u.Path = "/.well-known/oauth-protected-resource" + strings.TrimSuffix(u.Path, "/")
		u.RawQuery = ""
		rmURL = u.String()
	}
	var pr struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := c.getJSON(rmURL, &pr); err != nil {
		return nil, "", fmt.Errorf("resource metadata: %w", err)
	}
	if len(pr.AuthorizationServers) == 0 {
		return nil, "", errors.New("no authorization_servers in resource metadata")
	}
	issuer := strings.TrimSuffix(pr.AuthorizationServers[0], "/")
	var md asMeta
	if err := c.getJSON(issuer+"/.well-known/oauth-authorization-server", &md); err != nil || md.AuthorizationEndpoint == "" {
		if err2 := c.getJSON(issuer+"/.well-known/openid-configuration", &md); err2 != nil {
			return nil, "", fmt.Errorf("authorization server metadata: %v / %v", err, err2)
		}
	}
	if md.AuthorizationEndpoint == "" || md.TokenEndpoint == "" || md.RegistrationEndpoint == "" {
		return nil, "", errors.New("authorization server metadata incomplete")
	}
	resource := pr.Resource
	if resource == "" {
		resource = c.cfg.Endpoint
	}
	return &md, resource, nil
}

func (c *client) register(regURL, redirect string) (string, string, error) {
	payload, err := json.Marshal(map[string]any{
		"client_name":                "uni-cli",
		"redirect_uris":              []string{redirect},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	if err != nil {
		return "", "", err
	}
	resp, err := c.http.Post(regURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("register: http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", "", err
	}
	if out.ClientID == "" {
		return "", "", errors.New("registration response without client_id")
	}
	return out.ClientID, out.ClientSecret, nil
}

func (c *client) getJSON(u string, v any) error {
	resp, err := c.http.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.Unmarshal(b, v)
}

func (c *client) connect() error {
	if c.ready {
		return nil
	}
	if _, err := c.token(); err != nil {
		return err
	}
	res, err := c.rpc("initialize", map[string]any{
		"protocolVersion": protocol,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "uni-cli", "version": version},
	})
	if err != nil {
		return err
	}
	var ini struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(res, &ini) == nil && ini.ProtocolVersion != "" {
		c.proto = ini.ProtocolVersion
	}
	if err := c.notify("notifications/initialized", nil); err != nil {
		return err
	}
	c.ready = true
	return nil
}

func (c *client) tools() ([]tool, error) {
	var all []tool
	cursor := ""
	for {
		var params any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		res, err := c.rpc("tools/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools      []tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Tools...)
		if page.NextCursor == "" {
			return all, nil
		}
		cursor = page.NextCursor
	}
}

func (c *client) toolCall(name string, args any) (json.RawMessage, error) {
	params := map[string]any{"name": name}
	if args != nil {
		params["arguments"] = args
	}
	return c.rpc("tools/call", params)
}

func (c *client) rpc(method string, params any) (json.RawMessage, error) {
	id := c.next
	c.next++
	msg, err := c.post(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	if msg.Error != nil {
		return nil, fmt.Errorf("%s: %s (%d)", method, msg.Error.Message, msg.Error.Code)
	}
	return msg.Result, nil
}

func (c *client) notify(method string, params any) error {
	resp, err := c.do(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: http %d: %s", method, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (c *client) post(msg rpcRequest) (*rpcResponse, error) {
	resp, err := c.do(msg)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && c.cfg.APIKey == "" && c.cfg.OAuth != nil && c.cfg.OAuth.RefreshToken != "" {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err := c.refresh(); err != nil {
			return nil, err
		}
		if resp, err = c.do(msg); err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return readRPC(resp.Body, resp.Header.Get("Content-Type"), *msg.ID)
}

func (c *client) do(msg rpcRequest) (*http.Response, error) {
	token, err := c.token()
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	if c.proto != "" {
		req.Header.Set("MCP-Protocol-Version", c.proto)
	}
	if c.sid != "" {
		req.Header.Set("Mcp-Session-Id", c.sid)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sid = sid
	}
	return resp, nil
}

func readRPC(r io.Reader, contentType string, want int) (*rpcResponse, error) {
	if strings.Contains(contentType, "text/event-stream") {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
		var data []string
		for sc.Scan() {
			line := sc.Text()
			if line != "" {
				if v, ok := strings.CutPrefix(line, "data:"); ok {
					data = append(data, strings.TrimSpace(v))
				}
				continue
			}
			if len(data) == 0 {
				continue
			}
			var msg rpcResponse
			if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &msg); err != nil {
				return nil, fmt.Errorf("sse: %w", err)
			}
			data = data[:0]
			if msg.ID != nil && *msg.ID == want {
				return &msg, nil
			}
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("stream closed before response")
	}
	var msg rpcResponse
	if err := json.NewDecoder(r).Decode(&msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func output(res json.RawMessage, raw, table bool) error {
	if raw {
		return printJSON(res)
	}
	var tr toolResult
	if err := json.Unmarshal(res, &tr); err != nil {
		return err
	}
	var parts []string
	for _, p := range tr.Content {
		if p.Type == "text" && p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	text := strings.Join(parts, "\n")
	if tr.IsError {
		fmt.Fprintln(os.Stderr, text)
		return errors.New("tool call failed")
	}
	var list struct {
		Issues []struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Assignee string `json:"assignee"`
			Title    string `json:"title"`
		} `json:"issues"`
		Results []struct {
			ID        string `json:"id"`
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Title     string `json:"title"`
		} `json:"results"`
	}
	if table && json.Unmarshal([]byte(text), &list) == nil && (list.Issues != nil || list.Results != nil) {
		for _, i := range list.Issues {
			fmt.Printf("%-10s %-14s %-14s %s\n", i.ID, i.Status, cmp.Or(i.Assignee, "-"), i.Title)
		}
		for _, r := range list.Results {
			ts := cmp.Or(r.Timestamp, "-")
			fmt.Printf("%-36s %-10s %-10s %s\n", r.ID, cmp.Or(r.Type, "-"), ts[:min(10, len(ts))], cmp.Or(r.Title, "(untitled)"))
		}
		return nil
	}
	if pretty, ok := prettyJSON(text); ok {
		text = pretty
	}
	fmt.Println(text)
	return nil
}

func printJSON(raw json.RawMessage) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		fmt.Println(string(raw))
		return nil
	}
	fmt.Println(buf.String())
	return nil
}

func prettyJSON(s string) (string, bool) {
	var buf bytes.Buffer
	if json.Indent(&buf, []byte(strings.TrimSpace(s)), "", "  ") != nil {
		return "", false
	}
	return buf.String(), true
}

func attr(header, key string) string {
	key += "="
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, key) {
			return strings.Trim(strings.TrimPrefix(part, key), `"`)
		}
	}
	return ""
}

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func openBrowser(u string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("open", u).Start()
	case "windows":
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	default:
		_ = exec.Command("xdg-open", u).Start()
	}
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
