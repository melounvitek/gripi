# PLAN: Sidebar session awareness

## Context

The web gateway has become useful enough for real work. The remaining sidebar UX should make it easier to see what is happening across sessions without manual refreshes or hunting through project groups.

## Goals

1. Refresh the sidebar live enough that session state, titles, ordering, and indicators do not require manual page refreshes.
2. Show clear sidebar indicators for sessions that are currently in progress and sessions with unread activity from the assistant.
3. Add a top-level `Recent sessions` section showing the latest 5 sessions, with compact context indicating which folder/project each session belongs to.

## Desired UX

- The sidebar should update while the app is open, especially when session titles change, new sessions are created, a session starts/finishes work, or background sessions receive new output.
- A session that is actively being worked on should be visually distinguishable from idle sessions.
- A session with unread assistant activity should be visible at a glance without being visually noisy.
- The current session should not feel duplicated/confusing if it appears in both `Recent sessions` and its normal project group.
- Recent session rows should include enough project/folder information to disambiguate similarly named sessions from different checkouts.

## Design questions to answer first

- What source of truth should drive sidebar refreshes: existing `/events` polling, a separate sidebar metadata endpoint, or a lightweight combined refresh endpoint?
- What exactly counts as `in progress`?
  - An active RPC turn is running?
  - A tool call is currently executing?
  - The assistant is waiting for approval/input?
- What exactly counts as `unread`?
  - New assistant output in a non-active session?
  - Tool failure or approval-needed state in a non-active session?
  - Output that arrived after the user last opened or viewed the session?
- Should unread state be per browser/tab, persisted locally, or persisted server-side?
- When is unread cleared: opening the session, scrolling to bottom, focusing the tab, or sending a reply?
- Should `Recent sessions` duplicate sessions already shown below, or should normal project groups de-emphasize/filter recent sessions?

## Proposed implementation rounds

### Round 1: Audit current sidebar/session metadata

- Inspect sidebar rendering in `views/`.
- Inspect session metadata/discovery code in `lib/`.
- Inspect frontend polling/session-switching code.
- Identify existing title, cwd, timestamp, and active-session data.
- Decide the smallest metadata shape needed for sidebar refreshes.

### Round 2: Live sidebar refresh foundation

- Add or reuse an endpoint that returns server-rendered sidebar HTML or compact sidebar metadata.
- Refresh the sidebar after relevant events without a full page reload.
- Preserve expanded/collapsed cwd groups and selected session state where practical.
- Ensure session rename/title changes appear without manual refresh.

### Round 3: In-progress indicators

- Track and expose whether a session has an active turn or pending work.
- Render a calm indicator in the sidebar, such as a subtle dot/spinner/text badge.
- Verify active state appears and clears correctly across session switches and background sessions.

### Round 4: Unread indicators

- Define unread semantics and storage.
- Mark background sessions unread when meaningful assistant/tool activity arrives.
- Clear unread at the chosen read point.
- Render a readable but non-noisy unread indicator in the sidebar.

### Round 5: Recent sessions section

- Add a top `Recent sessions` section with the latest 5 sessions.
- Include compact cwd/project labels for each row.
- Decide and implement duplicate/current-session handling.
- Verify ordering updates when sessions are created, renamed, or receive activity.

## Validation

- Run focused automated tests where practical.
- Manually verify:
  - sidebar updates without full page refresh
  - active session indicator while a turn is running
  - unread indicator for background session activity
  - unread clearing behavior
  - recent sessions ordering and project labels
  - direct links, browser back/forward, and session switching
  - mobile/sidebar collapsed behavior if affected

## Notes

- Keep the sidebar calm and readable; indicators should guide attention, not create notification noise.
- Prefer small, incremental changes over a large sidebar rewrite.
- If code changes affect the running gateway, a restart will be needed, but do not restart automatically.
