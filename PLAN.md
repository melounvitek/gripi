# Plan: environment variables for Pi

## Goal

A user saves environment variables in Gripi, and Pi gets them, together with every command it runs. In single-user mode they apply to every session. In multi-user mode each user has their own set, applied only to the sessions that user owns. The main use is acting as yourself on GitHub from a shared gateway.

Mockups (untracked): `~/Work/gripi/tmp/mockups/env/` — `sheet-first.png`, `sheet-later.png`, `context-github.png`, `phone-github.png`, `sidebar-directions.png` (direction B), `sidebar-icons.png`, `header-mobile.png`. Their markup and styles are in `env.js`, `env.css`, `sidebar.js` and `sidebar.css` in the same folder.

## Rounds

Each round is one commit and leaves `mise run test` green.

1. [x] Pi gets the saved variables: storage, routes, validation, and the overlay on Pi's environment.
2. [ ] Changes apply from the next message: a stale idle Pi is restarted before the message is sent.
3. [ ] The bell: Notifications becomes an icon in the sidebar header.
4. [ ] The dialog: the key icon, the Ctrl+K command, and the Environment dialog.
5. [ ] Demo and docs. Delete this file.

## Storage

- One Gripi state file, `~/.pi/gripi/environment.json`, overridable with `GRIPI_ENVIRONMENT_PATH`, written through `internal/state` like the other state files.
- It holds, per user, an ordered list of name and value pairs and the time of the last change. Single-user mode uses the empty user ID; multi-user mode uses the workspace ID.
- A malformed file is left untouched. Requests that need it fail and the log names the file. Pi does not start for a session whose variables cannot be read, because starting it without them would act as the gateway's owner.
- Nothing Pi-owned changes.

## Validation

- A name matches `[A-Za-z_][A-Za-z0-9_]*`.
- `HOME`, `GRIPI_*` and `PI_CODING_AGENT_*` are rejected: they move Pi's or Gripi's directories.
- A value is not empty, has no NUL byte, and is at most 16 KiB. A user has at most 100 variables.

## Routes

All act on the current user, return JSON, and send `Cache-Control: no-store`. Follow the conventions of the existing form routes, such as the session tag routes.

| Route | Input | Result |
| --- | --- | --- |
| `GET /environment` | | `{"variables":[{"name":"GH_TOKEN"}]}`: names only, in saved order |
| `GET /environment/value?name=NAME` | | `{"value":"…"}`, or 404 |
| `POST /environment/variable` | `name`, `value`, optional `previous_name` | the list, or 422 `{"error":"…"}` |
| `POST /environment/variables` | `text`: `NAME=value` lines | the list and `"saved": N`, or 422 `{"error":"Line 6: …"}` |
| `POST /environment/variable/delete` | `name` | the list |

- Saving an existing name replaces its value in place. A new name is appended.
- `previous_name` renames in place. Renaming onto another saved name is an error.
- The block is all or nothing. Lines are trimmed; blank lines and lines starting with `#` are skipped; the name and value are split at the first `=` and trimmed; one pair of matching surrounding quotes is removed from the value, as `~/.config/gripi/env` does. A line without `=` and a name set twice in the block are errors.

Error texts:

- `“ASANA API KEY” is not a valid name. Use letters, digits and _.`
- `“GRIPI_PORT” is reserved for Gripi and Pi.`
- `GH_TOKEN has no value.`
- `GH_TOKEN is too long.`
- `GH_TOKEN is already set.`
- `At most 100 variables can be saved.`
- `Line 3: expected NAME=value.`
- `Line 7: GH_TOKEN is set twice.`
- Block errors start with `Line N: `.

## Pi's environment

- Pi starts with the gateway's environment, minus what is scrubbed today, with the session owner's variables on top.
- An existing session takes its owner from the ownership store. A new session takes the user who creates it. A multi-user session with no owner gets no variables.

## Changes apply from the next message

- Before a message or `!` command is sent to a session, Gripi checks whether that session's Pi process started before its owner's last change.
- If so, and the process is idle, Gripi retires it the way idle cleanup does, so the browser's event cursor stays valid, and the message starts a new process.
- A running turn is never interrupted. A new session that Pi has not saved yet is left alone; this is a known limit.

## The bell (sidebar header)

- The Notifications row leaves the sidebar's foot. A bell button sits in the sidebar header, after the title and before the hide and close buttons, inside a tools group that the key joins in round 4.
- On: the bell in the accent colour. Off or never enabled: grey with a slash. Blocked: the danger colour with a slash. The `title` and `aria-label` keep today's texts. The visible "Notifications" label and state text go away.
- The button is 2.25rem square, and 2.75rem on narrow or coarse-pointer screens. Hover styles apply only to fine pointers, so the first tap activates.

## The dialog

Opened by the key button in the header tools group (title `Environment variables`) and by `Environment…` in the Ctrl+K palette's Gripi group.

List:

- Description, single-user: `Pi and every command it runs get these variables in every Gripi session, on top of the gateway's own environment.`
- Description, multi-user: `Pi and every command it runs get these variables in your sessions only, on top of the gateway's own environment. They are stored on the gateway, where other users' Pi could read them.`
- One row per variable: the name and `••••••••`. Values are never in the list.
- `+ add variable`.
- A GitHub row while the ten GitHub names below are not all saved: `+ add GitHub variables` with the detail `gh, commits and push as you` when none is saved, otherwise `+ add the rest for GitHub`.
- A gold line when `GH_TOKEN` is saved and the set is incomplete: `GH_TOKEN covers gh only. Commits and git push still use the gateway's identity.`
- Status: `Nothing set yet.` when empty, otherwise `Changes apply from your next message in each session.` After a save: `Saved · used from your next message.` or `Saved N variables · used from your next message.` After a removal: `Removed · applies from your next message.` Errors use the danger colour.
- Hint: `↑↓ navigate · enter open · esc close`. There is no delete key; removal is in the row's form.

One variable (opening a row, or `+ add variable`):

- `Name:` and `Value:` inputs. An existing row loads its value from the server when opened.
- `save | remove | cancel`, without `remove` when adding. Hint: `enter save · esc cancel`. Esc closes the form, not the dialog.
- Pasting one `NAME=value` line into Name fills both inputs. Pasting several lines opens them as a block.

Block (pasted lines, or the GitHub row):

- A textarea that grows with its content, `save | cancel`, hint `ctrl+enter save · esc cancel`.
- Pasted lines: `One NAME=value per line. Lines starting with # are ignored.`
- GitHub: the title `GitHub: act as yourself` and `Fill in the empty values and save. Lines starting with # are ignored. Create the token at github.com/settings/tokens; a classic token needs the repo, read:org and gist scopes.`
- The GitHub block holds the lines whose names are not saved yet, with the comment of each group that still has a line:

```
# gh: pull requests, issues, API
GH_TOKEN=
# your name and email on commits
GIT_AUTHOR_NAME=
GIT_AUTHOR_EMAIL=
GIT_COMMITTER_NAME=
GIT_COMMITTER_EMAIL=
# git push over HTTPS with your token, also for SSH remotes; leave as is
GIT_CONFIG_COUNT=2
GIT_CONFIG_KEY_0=url.https://github.com/.insteadOf
GIT_CONFIG_VALUE_0=git@github.com:
GIT_CONFIG_KEY_1=credential.https://github.com.helper
GIT_CONFIG_VALUE_1=!gh auth git-credential
```

Touch: every row and button activates on the first tap and is at least 2.75rem tall on coarse pointers. On a phone the dialog is a bottom sheet like the other dialogs.

## Limits to document

- A new session with no message yet keeps the values it started with.
- Pi CLI in a terminal does not get the variables.
- In multi-user mode the variables separate identity, not secrets: every user runs as the same OS user.
- The GitHub block covers `git@github.com:` remotes, not `ssh://git@github.com/`.

## Deferred (for `TODO.md` in round 5)

- Personal skills per user in multi-user mode.
- A single GitHub row that fills in the variables from a token, or imports the login from the desktop app's computer.
