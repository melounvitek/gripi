# Tree navigation parity

## Goal

Bring GRIPi's `/tree` workflow close to practical Pi CLI parity while preserving Pi-owned session data and behavior.

## Scope

- [x] Retrieve and project Pi's native `get_tree` response.
- [x] Support no-summary, summary, and custom-instruction branch navigation.
- [x] Honor effective `treeFilterMode` and `branchSummary.skipPrompt` settings.
- [x] Read, set, replace, and clear native Pi labels.
- [x] Render a searchable, filterable, foldable visual tree with visible controls and keyboard navigation.

## Deferred

- Selected-entry clipboard commands.
- Global or double-Escape tree shortcut.
- Exact replication of every Pi branch-segment keybinding.
- Cancellation of an in-progress branch summary when native APIs do not expose it.
- Exhaustive rich previews for extension-defined entry types.

## TDD rounds

- [x] Native tree retrieval and bounded browser projection.
- [x] Configurable navigation, summaries, and effective settings.
- [x] Native label mutations.
- [x] Frontend tree model and modal interaction.
- [x] Full validation, independent simplification review, fixes, and repeat review.

## Constraints

- Keep mutations Pi-native through extension APIs.
- Do not put gateway metadata in Pi session files.
- Block tree mutations while a session is busy.
- Preserve existing drafts when navigating.
- Avoid raw images or unbounded tool output in browser payloads.
