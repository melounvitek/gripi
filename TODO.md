# Follow-up

- Fix Escape handling in session-only windows: `SessionActionsController.handleKeydown()` treats a missing session-actions menu as open and consumes Escape before the tag picker can close. This predates the compact header change. Cover it with a browser regression test; the picker's Close button works.
