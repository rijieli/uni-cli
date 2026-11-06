# uni-cli

> Not sure how to use it? Ask your coding agent to help.

Minimal command-line client for remote MCP servers, with presets for
[Linear](https://linear.app/docs/mcp) and [Notion](https://developers.notion.com/docs/mcp),
and local Slack desktop access on macOS.

## Install

```sh
brew install stillmake/tap/uni-cli
```

Or download the archive for your platform from [GitHub Releases](https://github.com/rijieli/uni-cli/releases), extract it, and put `uni-cli` on your `PATH`.

## Build

```sh
go build -o uni-cli .
```

## Usage

```sh
uni-cli <service> <command>

uni-cli linear login                  # OAuth in the browser (--read for read-only)
uni-cli linear status                 # login state and token expiry
uni-cli linear issues '{"limit": 20}' # one line per issue
uni-cli notion search "roadmap"       # one line per result
uni-cli notion tools                  # list MCP tools
uni-cli notion schema <tool>          # show a tool's input schema
uni-cli notion call <tool> '{...}'    # call any tool
uni-cli slack login miidii            # connect a Slack workspace on macOS
uni-cli slack read <url>              # read a message and thread
uni-cli slack search "query"          # search messages
uni-cli slack status                  # show connected workspace
uni-cli linear workflow               # standard workflow for coding agents
```

A binary named `linear-cli`, `notion-cli`, or `slack-cli` (copy or symlink) acts as `uni-cli linear`, `uni-cli notion`, or `uni-cli slack`.

Run `uni-cli --help` for all commands and flags.

## Config

`<service>.json` sits next to the binary and holds the endpoint and OAuth session.
Set `LINEAR_API_KEY` to use an API key instead of OAuth for Linear; Notion is OAuth only.

## Slack setup (macOS, experimental)

If Slack is signed into your workspace on this Mac, uni-cli can read the desktop
session directly:

```sh
uni-cli slack login <workspace-subdomain>
uni-cli slack read 'https://workspace.slack.com/archives/.../p...'
uni-cli slack search 'search terms'
```

For example, use `miidii` for `miidii.slack.com`. Login reads the Slack desktop
database and its cookie-encryption key from macOS Keychain, and saves the selected
workspace's session in Keychain as `uni-cli Slack desktop session`. It does not
write a session file. If Slack is running, login asks for confirmation before
closing it. macOS may ask you to allow Keychain access. After login,
Slack can be reopened. `uni-cli slack logout` removes the saved copy; it
does not revoke the Slack session.

This mode uses Slack's desktop session rather than OAuth scopes. It can access
data available to the signed-in user, relies on undocumented Slack behavior, and
may stop working when Slack changes. Only read and search are implemented. Check
your workspace's policies before using it.

Any other remote MCP server: write `{"endpoint": "https://..."}` to `<name>.json`
next to the binary, then run `uni-cli <name> login`.
