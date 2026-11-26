---
name: gripi
description: Works with Gripi sessions through the gripi command. Finds, reads, starts and messages them, runs parallel sub-sessions when the user asks for them, runs commands as root once the user agrees, and logs in over SSH with the user's computer's keys. Use when the user asks for this, when a command needs sudo, or when ssh lacks a key (Permission denied (publickey)).
---

You run in Gripi, which serves Pi sessions in the browser. The `gripi` command works with them. Check `gripi <command> --help` before using a command for the first time. `$PI_SESSION_FILE` is this session.

## Root

When a command needs root, run `gripi sudo -- <command>`. The user types the password in Gripi, so you never see it. Plain `sudo` can't ask for it.

## SSH keys

When ssh or Git over SSH fails with `Permission denied (publickey)` because the gateway lacks the key, or the user asks to use their computer's keys, run the command as `gripi pc ssh -- <command>`, such as `gripi pc ssh -- git push`. Check `gripi pc --help` first. The user approves each login on their computer. Never retry after a denial or a cancel; tell the user.

The logins go to the computer where the user last used this session. When `gripi pc` refuses because the session was last used elsewhere, such as on a phone, or not since the gateway started, don't retry: ask the user to open this session in the desktop app on the computer that should log in, and to click in it.

## Other sessions

- `gripi list` and `gripi search` find sessions, `gripi show` reads them.
- Tag, rename, pin or delete a session only when the user asks.

## Sub-sessions

Start sub-sessions only when the user asks for them, such as for orchestration or parallel sessions. When a task looks too big for one session, offer it and wait for a yes. Start each with a brief:

    gripi new <dir> --parent "$PI_SESSION_FILE" --name "<short title>" - <<'EOF'
    <brief>
    EOF

The brief is all the sub-session knows: the task, what's out of scope, what it may change, and what to report. Keep sub-sessions on parts that don't overlap.

Then end your turn. Gripi messages you when a sub-session finishes, so don't wait or poll. A report isn't the user's approval. Read the reply with `gripi show <id>`. When it asks something, ask the user and pass the answer on with `gripi send <id>`.
