# Domain Docs

## Before exploring the codebase

This repository has a single domain context. Read:
- `GLOSSARY.md` for domain terminology.
- Relevant decision records in `docs/adr/` for the current task.

If a file does not exist, continue silently without mentioning it or suggesting its creation. The `domain-modeling` skill creates these files as needed once a term or decision is settled.

## Use domain terminology

Use the terms defined in `GLOSSARY.md` in issue titles, refactoring proposals, hypotheses, and test names. Avoid synonyms the glossary explicitly excludes.

If a concept is missing, first check whether you are introducing terminology the project does not use. Record genuine gaps for the `domain-modeling` skill to address.

## Surface ADR conflicts

If your work conflicts with an existing ADR, state the ADR number, describe the conflict, and explain why the decision should be revisited. Do not silently override an existing decision.
