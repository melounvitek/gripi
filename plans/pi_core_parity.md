# Pi core parity improvements

## Goal

Close the highest-impact gaps an existing Pi user would notice in Gripi before a public announcement:

- clearly document Pi authentication and project-trust prerequisites;
- distinguish steering from follow-up messages while Pi is running;
- add Pi-like `@` fuzzy file references and ordinary path completion.

Project trust remains Pi-owned. Gripi will not add a trust extension or read/write Pi trust data; users must establish project trust in Pi CLI before using project-local Pi resources through Gripi.

## TDD rounds

1. Add regression coverage and documentation for Pi authentication, service-user credentials, and CLI project trust.
2. Add RPC/request coverage and UI behavior for steering versus follow-up submission.
3. Add a bounded, authorized path-suggestion backend for fuzzy `@` search and ordinary path completion.
4. Add composer autocomplete behavior, accessibility, mobile interaction, and session lifecycle coverage.
5. Run the full suite and final independent reviews; adopt worthwhile simplifications and rerun validation.

## Constraints

- Preserve native Pi data and workflows.
- Keep changes minimal and avoid a frontend build pipeline.
- Protect filesystem suggestion requests with existing browser/workspace and origin controls.
- Keep server-rendered and live/rebound composer behavior aligned.
- Do not restart the running Gripi service.
