# slim-mcp

> Not sure how to use it? Ask your coding agent to help.

Minimal command-line client for remote MCP servers, with presets for
[Linear](https://linear.app/docs/mcp) and [Notion](https://developers.notion.com/docs/mcp).

## Build

```sh
go build -o slim-mcp .
```

## Usage

```sh
slim-mcp <service> <command>

slim-mcp linear login                  # OAuth in the browser (--read for read-only)
slim-mcp linear status                 # login state and token expiry
slim-mcp linear issues '{"limit": 20}' # one line per issue
slim-mcp notion search "roadmap"       # one line per result
slim-mcp notion tools                  # list MCP tools
slim-mcp notion schema <tool>          # show a tool's input schema
slim-mcp notion call <tool> '{...}'    # call any tool
slim-mcp linear workflow               # standard workflow for coding agents
```

A binary named `linear-cli` or `notion-cli` (copy or symlink) acts as `slim-mcp linear` or `slim-mcp notion`.

Run `slim-mcp --help` for all commands and flags.

## Config

`<service>.json` sits next to the binary and holds the endpoint and OAuth session.
Set `LINEAR_API_KEY` to use an API key instead of OAuth for Linear; Notion is OAuth only.

Any other remote MCP server: write `{"endpoint": "https://..."}` to `<name>.json`
next to the binary, then run `slim-mcp <name> login`.
