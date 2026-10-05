# TODO

- Full-text session search. The sidebar search matches only a session's name, directory and first user message. Pi's `/resume` picker matches all message text, with fuzzy tokens, `"phrases"` and `re:` patterns. Matching it means indexing message text in the gateway, which costs memory.
- Palette commands that Pi runs through the composer: clone, compact, export, reload, and Pi's skills, prompt templates and extension commands. Running them today means submitting the composer, which would take a draft and its attachments with it. They need a way to run that leaves the composer alone.
- More palette commands: show only a project or tag, delete session, notifications, gateway update.
- A way into the palette without a keyboard, and an entry in the desktop app's menu.
- Faster first start. The gateway's first start, and any start after the saved session cache's version changes, still parses every session file one at a time: 28 s for 1.7 GB of sessions. Parsing them in parallel took 6.8 s with 4 workers and 4.0 s with 8 in a rough test, for about twice the memory (175 MB to 350 MB).
- Count HTTP errors from event polls as lost connection. "Connection lost. Retrying…" appears after two `/events` requests in a row fail outright. An error response, such as a proxy's 502 while the gateway is down, is retried silently, so behind a proxy a gateway that goes down mid-run shows no warning until the page is refocused.
