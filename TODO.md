# TODO

- Full-text session search. The sidebar search matches only a session's name, directory and first user message. Pi's `/resume` picker matches all message text, with fuzzy tokens, `"phrases"` and `re:` patterns. Matching it means indexing message text in the gateway, which costs memory.
