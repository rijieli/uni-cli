# Notion Workflow

A standard path for a coding agent using Notion as the source of specs and tasks.
Projects can extend it with their own steps (design, testing, commit format).
Tool arguments change over time; check them with `uni-cli notion schema <tool>`.

## 0. Check the session

```sh
uni-cli notion status
```

If not logged in, or expired with no refresh token, ask the user to run `uni-cli notion login`.

## 1. Find the page

```sh
uni-cli notion search "<keywords>"
```

- If the user gave a Notion URL or page ID, skip the search and fetch it directly.
- For a task database, fetch the database first; its data source URL lets you search inside it.

## 2. Read the whole page

```sh
uni-cli notion call notion-fetch '{"id": "<URL-OR-ID>"}'
uni-cli notion call notion-get-comments '{"page_id": "<PAGE-ID>"}'
```

- Follow linked and child pages that hold requirements.
- Comments often hold extra requirements and decisions.
- Image and file links are signed and expire quickly. Download them right away into a new temporary folder, then view them.
- If the requirements are still unclear, ask the user before writing code.

## 3. Implement

Keep changes narrow, and verify them the way the project expects.

## 4. Commit and push

- One task per commit. Stage only that task's files.
- Push **only when the user allows it**.

## 5. Update Notion

Edit pages **only when the user allows it**. Prefer a comment over rewriting the spec:

```sh
uni-cli notion call notion-create-comment '{"parent": {"page_id": "<PAGE-ID>"}, "rich_text": [{"text": {"content": "<summary>"}}]}'
```

For a task database, update the status property:

```sh
uni-cli notion schema notion-update-page
uni-cli notion call notion-update-page '{...}'
```
