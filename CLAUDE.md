# Claude Repo Notes

## Git Commit

- Before every commit, in addition to using the `commit-message-convention` skill, inspect `git status --short`, `git diff --staged --name-only`, and patch-level diffs when needed. Confirm that the commit contains only changes related to the current task.
- Do not commit unrelated files, temporary files, accidental formatting-only output, build artifacts, or untracked directories by default. If multiple intents exist, split them into separate commits.
- Continue using Conventional Commits. Default to Chinese for commit messages unless the user explicitly asks for English. The subject must state the primary purpose clearly and must not use vague text such as `update`, `fix issue`, or `misc`.
- Prefer concise commit messages by default. Use `subject` only for small or single-purpose changes; add a `body` only when the reason, constraints, risks, or affected scope would otherwise be unclear.
- When a commit body is needed, keep it short and scannable with concrete bullets only. Prefer 1-3 bullets, avoid repeating obvious diff details, and do not include long narratives, process descriptions, or padded explanation.
- Run validation commands that match the scope of the change before committing. At minimum, type checks, tests, or builds affected by the change must be reasonably accounted for in the current context.
- If the repository provides a commit message lint script, run it before `git commit`.

## Code Comments

- When writing, modifying, or reviewing code for comments, actively scan for constants, important variables, methods/functions, key branches, async flows, lifecycle cleanup, and cross-component or cross-context links. Add comments by default for these targets so maintainers can understand intent without reconstructing context from call chains.
- Comments must explain intent, constraints, side effects, units, boundary meanings, data ownership, update flows, business impact, compatibility requirements, or why a specific approach is required. Do not merely restate names, types, or the obvious operation performed by the next line.
- Constants should have comments explaining purpose, unit, source, tuning rationale, compatibility meaning, boundary effect, or why the value should not be changed casually. Skip only truly self-explanatory display labels, obvious enum labels, and one-off local aliases.
- Important variables should have comments explaining what state or data they represent, which flow updates them, which logic consumes them, and what can go wrong if they become stale. This especially applies to reactive state, cached data, request tokens, permission flags, form models, selection state, current item pointers, pending task handles, and parent-child linkage state. Skip only short-lived local temporaries whose meaning is fully obvious at the usage site.
- Methods/functions should have comments or JSDoc explaining responsibility, call scenario, key parameters, side effects, and result impact. Public/exported functions, composables/hooks, event handlers, request wrappers, adapters, and non-trivial helpers should be documented even when their names are clear. Skip only tiny local helpers or direct wrappers where the name and signature already fully communicate the intent.
- Key branches should have comments explaining why the condition exists and how hitting or missing the branch affects business behavior, state consistency, permissions, validation, compatibility, race-condition protection, stale-data protection, corrupted-input handling, or fallback behavior.
- Asynchronous requests, concurrency control, debounce/throttle, cache invalidation, state reset, dialog lifecycle, component unmount cleanup, storage cleanup, and event listener registration/removal must document the intent at the decision or cleanup point.
- Business rules, permission control, form validation, backend field mapping, default values, dialog lifecycle, parent-child component linkage, browser-extension boundaries, and third-party component adaptation should document constraints and design reasons rather than only implementation details.
- When adding comments to existing files, preserve behavior and formatting, update nearby stale comments, remove misleading or duplicated comments, and avoid broad file-level narration unless the file defines a protocol, state machine, integration boundary, or non-trivial workflow.
- Before finishing a comment-focused change, re-scan the touched file for uncommented constants, important state, exported methods, key branches, async cleanup, and stale comments; add missing intent comments or explicitly leave obvious code uncommented.

## Element Plus Forms

- Whenever adding or modifying a form built with `el-form`, default to `FormInstance + FormRules` for validation.
- Do not use ad hoc required checks such as `if (!value) ElMessage.warning(...)` as the primary validation approach.
- On submit, validate through `await formRef.value?.validate()` first and return immediately if validation fails.
- Declare `prop` on each validated `ElFormItem`, and bind the corresponding `:rules` on `ElForm`.
- When clearing, closing dialogs, or resetting forms, also call `resetFields()` or `clearValidate()` as appropriate to avoid stale validation state.
- Use custom `validator` rules only for cross-field, asynchronous, or business-driven validation that cannot be expressed with basic rules.

## Dialog Conventions

- Use `AppDialog` as the default dialog component. Do not use raw `ElDialog` unless the case clearly requires it and the reason is documented.
- Dialogs must expose a unified `toggleDialog(open, payload?)` method. Parent components open dialogs only through the dialog ref and must not mutate internal dialog business state directly.
- Dialog initialization must live inside the dialog itself and run directly in `toggleDialog(true, payload)`. Do not use `watch(visible)` for initialization, prefilling, or reset logic.
- `handleCancel` is primarily responsible for setting `visible = false` to close the dialog.
- `handleClose` is primarily responsible for cleaning up side effects, including form reset, context cleanup, validation cleanup, request invalidation, loading reset, and cache cleanup.
- `toggleDialog(false)`, cancel-button close, top-right close, and post-submit close should all reuse the same close flow so cleanup stays consistent.
- If a dialog contains an `el-form`, use `formRef + FormRules` by default, validate before submit, and run `resetFields()` or `clearValidate()` during cleanup.
- Async results from stale requests must not write back into a dialog after it has been closed or reinitialized.

## Write Tool Usage Guidelines

The Write tool itself supports reasonably large files (verified up to 20-30KB), but is constrained by the AI assistant's single-response output token limit.

**File Size Recommendations:**
- **< 20KB**: Use Write tool directly - safe and reliable
- **20-50KB**: Use Bash heredoc for safer handling
- **> 50KB**: Must use Bash heredoc with segmented writes or append operations

**Common pattern for large files:**
```bash
# Initial write
cat > 'filepath' << 'EOF'
[First section content]
EOF

# Append additional sections
cat >> 'filepath' << 'EOF'
[Additional content]
EOF
```

**Why this matters:** When preparing to write very large content, the AI's output may be truncated mid-tool-call, resulting in an incomplete `<invoke name="Write"></invoke>` that fails with "missing parameter" errors. This is not a Write tool limitation - it's an output token budget constraint.
