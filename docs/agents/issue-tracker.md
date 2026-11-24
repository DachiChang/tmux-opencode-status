# Issue tracker: GitHub

Issues and specs live in GitHub Issues for `DachiChang/tmux-opencode-status`. Run `gh` from the repository directory.

## Issue operations

- Before creating the first issue, verify the target with `gh repo view --json nameWithOwner`.
  Create issues only in the user's own repository. If the target differs from the expected repository, confirm with the user first.
- Create: `gh issue create --title "..." --body-file -`; pass a multiline body with a heredoc.
- Read: `gh issue view <number> --json number,title,body,labels,comments`.
- List: `gh issue list --state open --json number,title,body,labels,comments`; add `--label` or adjust `--state` as needed.
- Comment: `gh issue comment <number> --body "..."`.
- Label: `gh issue edit <number> --add-label "..."` or use `--remove-label`.
- Close: Leave an explanatory comment before running `gh issue close <number>`. For an already closed issue, add any further explanation with a separate comment command.

## Sub-issues and dependencies

Prefer GitHub's native sub-issues and issue dependencies. Check `gh help` for commands supported by the installed version before using them.

If native relationships are unavailable:
- Put `Part of #<parent>` at the top of each child issue and add a task list of child issues to the parent.
- Put `Blocked by: #<n>, #<n>` at the top of the blocked issue.
- Start work on a blocked issue only after all blockers are closed.

## Pull requests as a triage surface

**PRs as a request surface: no.**

## Skill instruction mapping

- `publish to the issue tracker` means create a GitHub issue.
- `fetch the relevant ticket` means read the corresponding GitHub issue.

## Wayfinding

- Map: A single issue labeled `wayfinder:map` with Notes, Decisions-so-far, and Fog sections.
- Child ticket: Link it to the map as a sub-issue, or use the fallback above. Apply `wayfinder:<type>`, where `<type>` is `research`, `prototype`, `grilling`, or `task`.
- Frontier: In map order, choose the first open, unassigned child issue with no open blockers.
- Claim: Run `gh issue edit <n> --add-assignee @me` as the session's first write.
- Resolve: Comment with the answer, close the issue, then add a summary and link to the map's Decisions-so-far section.
