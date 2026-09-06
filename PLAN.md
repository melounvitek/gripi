# Session tags

Approved design: compact tag filter replaces Tags with the selected name and a separate clear action. One active tag combines with project/search filters. Pins retain their existing filter bypass; filtering never switches the open conversation. Multiple neutral clickable chips per session; separate searchable add/create/remove picker with immediate persistence, responsive bottom sheet, keyboard and first-tap touch access. Tags are shared across projects within user visibility, stored outside Pi files. Case-insensitive reuse. Unused tags disappear from suggestions. Forks/clones inherit tags. New-session dialog prefills only the active filter tag, visibly removable.

## TDD rounds

- [x] 1. Persistence/API: validation, persistence, isolated suggestions and authorization.
- [x] 2. Filtering/UI: compact controls, chips and editor, counts, URL/history and fragment consistency.
- [ ] 3. Lifecycle: new-session drafts, inherited fork/clone tags, pending path materialization, rename and deletion.
- [ ] 4. Browser hardening: touch, keyboard, navigation/polling races, responsive screenshots.

Commit each round after passing its tests. Prefer request/browser behavior coverage. Keep mockups in their separate worktree; no production Pi format changes. Use host tools through Mise and isolated E2E servers; do not restart gripi.service. Run final independent review, address useful simplifications, then remove this finished plan. No push or PR actions.
