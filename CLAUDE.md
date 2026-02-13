# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Protocol Documentation Reference

**Always consult the documentation files first** — Claude's training data may lack sufficient detail on these specific protocols.

### Zcash Protocol Spec

- `docs/papers/zcash-protocol-index.md` - Section index with line ranges (read this FIRST)
- `docs/papers/zcash-protocol.tex` - Full protocol spec in LaTeX (read specific line ranges from index)

## Code Style

- Prefer explicit type annotations for public APIs
- Use `#[must_use]` on functions returning Results
- Avoid `unwrap()` in library code, use `expect()` with message or propagate errors
- Do not delete existing comments unless they are factually wrong or reference removed code. When modifying code near comments, preserve them. When adding new code alongside existing commented code, add comments for the new code at the same level of detail.

## Debugging

- Add `dbg!()` for quick inspection (remove before commit)
- Run single test with output: `cargo test test_name -- --nocapture`
- Check NTT correctness: compare against naive polynomial multiplication in tests

## Pull Request Guidelines

When writing PR descriptions, always describe changes relative to the target branch (e.g., main), not the iterative steps taken during development. The PR description should reflect the actual diff - what code exists in the target branch vs what code exists in the PR branch. Avoid phrases like "renamed X to Y" unless X actually exists in the target branch.

**After pushing to a branch**, always check if there is an open PR for that branch. If so, review whether the PR title and description still accurately reflect the current state of the changes. If the pushed commits have changed the scope or nature of the PR, update the title and/or description accordingly using `gh pr edit`.

## Database Migrations

Do not create new migration files (e.g., `002_*.sql`). This is a pre-production codebase — modify the existing `001_init.sql` directly. Only create separate migrations if explicitly asked.

## Code Change Guidelines

**Never consider backwards compatibility** unless explicitly told to do so. Feel free to rename functions, change APIs, delete unused code, and refactor without worrying about breaking existing consumers. This is a research codebase where clean code matters more than stability.

- Describe changes relative to main branch, not iterative development steps
- Include benchmark results if touching performance-critical code
- Security-impacting changes require extra review
- PR description should reflect actual diff (what exists in main vs PR branch)
