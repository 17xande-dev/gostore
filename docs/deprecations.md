# Deprecations

Behaviour kept only so that something made against an older version keeps working.
Everything listed here is **removed at the next major version** — `v1.0.0` while releases
are `v0.x` — so check this page before tagging one.

Nothing is deprecated yet.

| Since | What | Instead | In the code |
|---|---|---|---|

## Adding one

Prefer not to: until `v1.0.0` nothing has promised compatibility, so a change can simply
replace what it changes. When something does have to keep working for a while:

1. Mark it in the code with a `Deprecated:` comment that says "remove at the next major
   version", so `rg 'Deprecated:'` finds every one at release time.
2. Add a row to the table above.
3. Say in the commit message what it keeps working, and for whom.

At a major release, remove everything in the table, then empty it.
