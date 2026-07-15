# Simplify session tree UI

## Goal

Make `/tree` focused and readable on desktop and mobile while preserving Pi-native behavior.

## TDD rounds

- [x] Collapse search, filters, labels, label timestamps, and keyboard help under a `Search & options` disclosure.
- [x] Flatten visual rendering so single-child chains remain aligned and indentation grows only around forks, matching Pi CLI semantics.
- [x] Cap narrow-screen branch indentation without horizontal scrolling.
- [x] Remove the redundant Active badge and only show Latest when it differs from Current.
- [x] Make the tree the primary scroll region while keeping modal header and actions reachable.
- [x] Preserve shortcuts by opening hidden options before focusing their controls.
- [x] Run the complete suite and independent review.
