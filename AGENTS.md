# Agent Instructions

## Product specification and implementation plan

Use `SPEC.md` as the authoritative product, API, data, and protocol specification.
Use `PLAN.md` as the phased implementation plan for turning the specification into code.
When implementing or reviewing changes, keep behavior aligned with both documents; update them when intentional design or plan changes are made.

## Code review order document

Keep `CODE_REVIEW_ORDER.md` up to date.

Whenever you add, remove, rename, or reorganize repository files, update `CODE_REVIEW_ORDER.md` so it continues to list the codebase in topological library-consumer review order:

1. Foundations and dependency declarations first.
2. Leaf/internal libraries before their consumers.
3. Package tests next to the package they exercise.
4. Executables and other imperative-shell entrypoints after the libraries they consume.
5. Runtime examples and operator documentation after implementation files.

Do not include generated runtime data, build outputs, `.git/`, or scratch-only files under `.local/` in the review order.
