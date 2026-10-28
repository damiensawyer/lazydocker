# Editing Best Practices (Ultra)

## Principles
- **Surgical:** Small, targeted edits only.
- **Read First:** `read` → analyze → `edit`. Never guess.
- **Exact Match:** Byte-for-byte. Whitespace/case/newlines matter.
- **One File/Call:** One file per `edit` command.
- **Efficiency:** Use `offset`/`limit`. Minimized `oldText`.
- **No Bloat:** No large unchanged blocks in `oldText`.
- **Edit vs Write:** `edit` = small changes; `write` = new/complete replacement.
- **Verify:** `read` + `test` after every edit.
- **Failure:** `edit` fail? STOP. `read` → analyze → retry.
- **Overflow:** Context full? STOP. Summarize → Reset.

## Workflow
`read` → `edit` → `read` → `test`

## Golden Rules
1. **Read before edit**
2. **Small edits**
3. **Exact match**
4. **Test early**
5. **Re-read on fail**
