---
description: Wrap up repo work: commit, merge, push, clean branch, restart if needed
---
Wrap up the current work for this repo.

1. Check git status and current branch.
2. If there are TODO/PLAN checklist items related to this work, ensure completed items are checked before committing.
3. If there are uncommitted changes:
   - inspect the diff
   - run focused tests if appropriate
   - create one or more small commits with clear imperative commit messages
4. If not already on `master`:
   - merge the completed work to `master` safely
   - push `master`
   - delete the feature branch if it was merged and is no longer needed
5. If already on `master`:
   - push `master`
6. If code changes require the local server to restart, use the safe pi-web-gateway restart script from `AGENTS.md` as the final step, then verify the server responds.
7. Report:
   - commits created
   - branch/merge/push result
   - whether server restart was performed
