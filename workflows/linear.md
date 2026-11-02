# Linear Workflow

A standard path for a coding agent taking a Linear issue from pick to push.
Projects can extend it with their own steps (design, testing, commit format).

## 0. Check the session

```sh
uni-cli linear status
```

If not logged in, or expired with no refresh token, ask the user to run `uni-cli linear login`.

## 1. Pick an issue

```sh
uni-cli linear issues --json '{"parentId": "<PARENT-ID>", "assignee": "<name>", "limit": 50}'
```

- Sort open issues by priority: Urgent > High > Medium > Low > No priority.
- Suggest the highest. If the user turns it down, suggest the next.
- When work starts, mark it In Progress:

```sh
uni-cli linear call save_issue '{"id": "<ISSUE-ID>", "state": "In Progress"}'
```

## 2. Read the whole issue

```sh
uni-cli linear call get_issue '{"id": "<ISSUE-ID>"}'
uni-cli linear call list_comments '{"issueId": "<ISSUE-ID>"}'
```

- Comments often hold extra requirements and links.
- Image links are signed and expire quickly. Download them right away into a new temporary folder, then view them.
- If the requirements are still unclear, ask the user before writing code.

## 3. Implement

Keep changes narrow, and verify them the way the project expects.

## 4. Commit and push

- One issue per commit. Stage only that issue's files.
- Start the message with the issue ID: `<ISSUE-ID> <short description>`.
- Push **only when the user allows it**.

## 5. Update the status

```sh
uni-cli linear call save_issue '{"id": "<ISSUE-ID>", "state": "In Review"}'
```

If the change still needs testing you couldn't do, say so and let the user choose the state.
