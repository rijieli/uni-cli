# uni-cli

> Not sure how to use it? Ask your coding agent to help.

Minimal command-line client for remote MCP servers, with presets for
[Linear](https://linear.app/docs/mcp) and [Notion](https://developers.notion.com/docs/mcp).

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
uni-cli linear workflow               # standard workflow for coding agents
```

A binary named `linear-cli` or `notion-cli` (copy or symlink) acts as `uni-cli linear` or `uni-cli notion`.

Run `uni-cli --help` for all commands and flags.

## Config

`<service>.json` sits next to the binary and holds the endpoint and OAuth session.
Set `LINEAR_API_KEY` to use an API key instead of OAuth for Linear; Notion is OAuth only.

Any other remote MCP server: write `{"endpoint": "https://..."}` to `<name>.json`
next to the binary, then run `uni-cli <name> login`.
