# Code Review File Order

Date: 2026-06-30

This document lists repository files in dependency-first review order. Local scratch files under `.local/`, generated runtime data under `.data/`, VCS metadata under `.git/`, and build outputs are intentionally excluded from the codebase review order.

## Topological library-consumer order

1. `SPEC.md`
   - Product, API, lifecycle, data, and encryption protocol specification that guides future implementation.

2. `PLAN.md`
   - Phased implementation plan derived from the design notes and `SPEC.md`.

3. `CODE_REVIEW_ORDER.md`
   - This review-order index. Update it whenever repository files change.

4. `AGENTS.md`
   - Agent instructions, including the reminder to maintain this review-order document.

5. `CLAUDE.md`
   - Symlink to `AGENTS.md` for Claude-compatible agent instruction discovery.
