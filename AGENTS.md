## Project language

Use English by default for new repository documentation, GitHub issues, and pull requests. Conversation with the user can remain in Traditional Chinese.

## Agent skills

### Issue tracker

Use GitHub Issues for issues and specs. Read `docs/agents/issue-tracker.md` before working with the issue tracker.

### Triage labels

Read `docs/agents/triage-labels.md` before triaging issues. Use the five default triage labels.

### Domain docs

This is a single-context repository. Read `docs/agents/domain.md` before exploring the codebase.

## Workflow rules

- During a grilling session, update only `GLOSSARY.md` and ADRs. Create specs only through `/to-spec`.
- Build prototypes in a separate worktree on a `prototype/<name>` branch. Commit the finished prototype without merging it into `main`.
  After confirmation, remove the worktree and stop any services the prototype started.
- If the direction of an implementation issue changes and diverges from the spec, pause. Quote the relevant spec text and tell the user what changed, how it differs, and which unfinished issues are affected.
  Once the user approves, update the spec to match the new implementation direction.
- After committing on an implementation branch, identify the branch it was created from (the base branch). Run `git merge-tree --write-tree --name-only <base-branch> HEAD` to check whether it can merge cleanly into its base branch, then report the result (exit 0: clean; exit 1: conflicting files are listed).
  If the base branch cannot be confirmed or the check finds conflicts, ask the user before merging `main` to resolve conflicts.
