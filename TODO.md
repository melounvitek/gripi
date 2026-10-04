# TODO

- Full-text session search. The sidebar search matches only a session's name, directory and first user message. Pi's `/resume` picker matches all message text, with fuzzy tokens, `"phrases"` and `re:` patterns. Matching it means indexing message text in the gateway, which costs memory.
- Palette commands that Pi runs through the composer: clone, compact, export, reload, and Pi's skills, prompt templates and extension commands. Running them today means submitting the composer, which would take a draft and its attachments with it. They need a way to run that leaves the composer alone.
- More palette commands: show only a project or tag, delete session, notifications, gateway update.
- A way into the palette without a keyboard, and an entry in the desktop app's menu.
