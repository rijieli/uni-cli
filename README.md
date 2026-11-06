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
uni-cli slack login                   # connect a Slack workspace on macOS
uni-cli slack read <url>              # read a message and thread
uni-cli slack search "query"          # search messages
uni-cli slack status                  # show connected workspace
uni-cli linear workflow               # standard workflow for coding agents
```

A binary named `linear-cli`, `notion-cli`, or `slack-cli` (copy or symlink) acts as `uni-cli linear`, `uni-cli notion`, or `uni-cli slack`.

Run `uni-cli --help` for all commands and flags.

## Config

On macOS, Linear and Notion configuration and credentials are stored in macOS
Keychain as `uni-cli MCP configuration`, with the service name as the account.
The first time you use each service, uni-cli migrates its existing JSON config
from beside the binary to Keychain and removes the file after verifying the copy.
If Keychain access fails or an existing Keychain entry differs from the file,
the file is kept and an error is shown. macOS may request Keychain access.

Set `LINEAR_API_KEY` to use an API key instead of OAuth for Linear; Notion is
OAuth only. Environment variables are not saved in Keychain. On Linux and
Windows, Linear and Notion continue to use `<service>.json` beside the binary.
Custom remote MCP servers use `<name>.json` on all platforms: write
`{"endpoint": "https://..."}` there, then run `uni-cli <name> login`.
Results explicitly stored with `--save` remain in `<service>-last.json` beside
the binary. Slack stores its session separately in macOS Keychain.

## Slack setup (macOS, experimental)

If Slack is signed into your workspace on this Mac, uni-cli can read the desktop
session directly:

```sh
uni-cli slack login
uni-cli slack read 'https://workspace.slack.com/archives/.../p...'
uni-cli slack search 'search terms'
```

Login selects your signed-in workspace, or prompts you to choose if there are
several. You can also provide a workspace subdomain as an optional argument.
Login reads the Slack desktop database and its cookie-encryption key from macOS
Keychain, and saves the selected
workspace's session in Keychain as `uni-cli Slack desktop session`. It does not
write a session file. If Slack is running, login asks for confirmation before
closing it. macOS may ask you to allow Keychain access. After login,
Slack can be reopened. `uni-cli slack status` shows the saved workspace without
checking whether the session is still valid. `uni-cli slack logout` removes the
saved copy; it does not revoke the original Slack session. If the session expires,
sign in to the Slack app and run `uni-cli slack login` again.

If Slack's cookie database needs recovery after closing, login briefly creates an
owner-only temporary copy of the database and its recovery files. SQLite recovers
the copy, leaving Slack's original database unchanged. The copy is removed when
login finishes.

This mode uses Slack's desktop session rather than OAuth scopes. It can access
data available to the signed-in user, relies on undocumented Slack behavior, and
may stop working when Slack changes. Only read and search are implemented. Check
your workspace's policies before using it. `read` returns up to 100 messages in
a thread, and `search` returns up to 20 results. Both print JSON.
