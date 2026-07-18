# Pi core parity improvements

## Goal

Close the highest-impact gaps an existing Pi user would notice in Gripi before a public announcement:

- clearly document Pi authentication and project-resource approval behavior;
- distinguish steering from follow-up messages while Pi is running;
- add Pi-like `@` fuzzy file references and ordinary path completion.

Gripi originally left project trust entirely to Pi CLI. A later usability decision changed Gripi to pass Pi’s native `--approve` flag by default for each RPC process, with `GRIPI_AUTO_APPROVE_PROJECTS=0` restoring Pi’s normal trust resolution. Gripi still does not read or write Pi’s saved trust data.

## TDD rounds

1. Add regression coverage and documentation for Pi authentication, service-user credentials, and project-resource approval.
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
