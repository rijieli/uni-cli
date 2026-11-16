# Whimsical Workflow

A standard path for a coding agent using Whimsical boards and docs as design or planning context.
Tool arguments change over time; check them with `uni-cli whimsical schema <tool>`.

## 0. Check the session

```sh
uni-cli whimsical status
```

If not logged in, or expired, ask the user to run `uni-cli whimsical login`.
Whimsical does not currently issue refresh tokens.

## 1. Find the file

```sh
uni-cli whimsical search "<keywords>"
```

- If the user gave a Whimsical URL or file ID, skip search and fetch it directly.
- If the workspace hierarchy is known but the title is not, inspect the `file_tree` schema and use it.
- Prefer the link the user supplied when the workspace contains similar titles.

## 2. Read the content

```sh
uni-cli whimsical call fetch '{"id": "<URL-OR-ID>"}'
```

- Boards and docs can be paginated; follow the returned cursor when more context is needed.
- For a board image, call `fetch` with `{"id": "<ID>", "image": true}`.
- Read linked docs or pages that hold the actual requirements.
- If the design or requirements are still unclear, ask the user before writing code.

## 3. Implement

Keep changes narrow, and verify them the way the project expects.

## 4. Create or update Whimsical

Change Whimsical content **only when the user allows it**. Call `how_to` before
creating a complex board, then check the exact schema before editing:

```sh
uni-cli whimsical schema how_to
uni-cli whimsical call how_to '{...}'
uni-cli whimsical schema edit
uni-cli whimsical call edit '{...}'
```

Do not delete, move, or rename files without explicit user consent. Prefer a
comment over rewriting a spec:

```sh
uni-cli whimsical schema comment_edit
uni-cli whimsical call comment_edit '{...}'
```
