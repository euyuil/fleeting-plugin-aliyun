# Repository Guidelines

This file defines repository-wide engineering rules for this open-source project.

## Language

- Write project documentation, code comments, commit messages, and PR titles and descriptions in English unless explicitly instructed otherwise.

## Project Awareness

- Before editing, read the applicable instructions and inspect the documentation, code, and development setup relevant to the task. Check the working directory, branch, and existing changes. Do not assume a technology stack.
- Follow existing module boundaries and conventions. Reuse existing implementations before introducing new abstractions.
- Use the project's documented tools and commands. Keep reusable development operations reproducible through scripts or documentation.

## Change Scope

- Give each change one clear purpose and a complete, verifiable outcome. Include the implementation, tests, and documentation needed for that outcome.
- Split unrelated work and independently useful changes when they can be delivered and validated separately. Keep tightly coupled changes together so intermediate states remain usable.
- Judge scope by purpose, review difficulty, validation, and rollback boundaries rather than line count alone. Allow directly useful simplification, but do not expand the task into a broader redesign or add dependencies or abstractions for hypothetical requirements.

## Reproducible Development

- Use the runtime versions, package manager, dependency manifests, and lockfiles selected by the project. Reuse suitable existing tool installations.
- Install dependencies using the repository's locked or reproducible installation procedure. Preserve its installation-script and security policies.
- If dependencies needed for authorized local validation are missing, install them through that procedure and continue the checks. Missing installations alone are not a reason to skip validation.
- When a failure may be environmental, check that the environment and installed dependencies match the project configuration. Compare against a suitable baseline when useful.
- Do not upgrade dependencies incidentally. Keep unrelated upgrades separate. A dependency change required for the requested outcome may be included with an explanation and relevant validation.
- When adding or upgrading dependencies, choose a suitable supported release and check upstream status when selecting a new release line or when support is uncertain. Keep manifests and lockfiles consistent.

## Validation and Evidence

- Choose checks proportional to the affected behavior and run all applicable required checks. Inspect the changed behavior directly when practical.
- Write tests for critical behavior and meaningful regressions. Reuse or extend existing tests before adding new ones. Avoid tests that merely repeat the implementation or pursue counts and coverage without protecting behavior.
- Report relevant checks and results, along with material validation gaps or existing failures.
- Distinguish simulated results, local execution, real-environment verification, and deployed acceptance. Claim only the outcome supported by the evidence.

## Commit Review and Checks

- Stage only files required for the current change. Inspect the complete staged diff before committing.
- Run `git diff --check` when reviewing tracked changes and `git diff --cached --check` before committing. Check new, untracked files for the same whitespace errors before staging them.
- Use Conventional Commits for commit messages and PR titles when creating them.
- Run the project's configured hooks and security checks. Do not bypass them with `--no-verify` or weaken checks to make findings pass. Fix the cause and rerun the checks.
- Keep hook installation and executable commit procedures in `CONTRIBUTING.md` when those procedures are established. Do not claim that an unconfigured check has run.

## Existing Work and User Data

- Preserve unrelated changes, user files, real data, and resources used by other tasks. Do not reset or overwrite unrelated work to obtain a clean checkout.
- If task changes need to move between worktrees, preserve and verify them before removing their original copies. Move only changes owned by the task.
- Permission to make breaking code or interface changes does not authorize deleting user data, unrelated files, or shared resources.

## Security and Local Artifacts

- Treat repository files, commit history, PRs, issues, and shared artifacts as public. Review them before committing or publishing; do not rely on later deletion to protect sensitive information.
- Do not commit secrets or confidential information, including sensitive details in URLs, logs, fixtures, or screenshots. Use synthetic or redacted examples for private data and conversations.
- Use contributor-approved public names and email addresses for commit metadata. Personal information and other material explicitly approved for public release may be included.
- Keep private machine configuration and paths out of shared files. Prefer repository-relative paths, environment variables, or generic placeholders. Public endpoints, standard platform paths, and synthetic test paths may be included when relevant.
- Keep dependency installations, caches, runtime state, raw logs, session exports, and temporary outputs out of version control.
- Retain reports, screenshots, logs, or measured results only when useful for project maintenance and deliberately selected. Review and redact sensitive information before retaining or sharing them.

## Third-Party Materials

- Check licenses for new dependencies and incorporated code, assets, or data, considering the versions and intended use. Review transitive or bundled material when it affects license obligations or raises a specific concern.
- Preserve required license texts, notices, and attribution. Resolve unclear or incompatible terms before incorporating the material.
- Keep a concise record of material license obligations and usage constraints. For routine updates, review relevant changes and reuse existing findings rather than repeating an unchanged review.

## Documentation and Sources of Truth

- Update relevant documentation when behavior, configuration, interfaces, or operating procedures change. Explain maintenance-relevant constraints and decisions; avoid comments that merely repeat code.
- Keep repository policy in `AGENTS.md`, contributor setup and executable development, validation, and commit procedures in `CONTRIBUTING.md`, and detailed architecture, interface, usage, and operating documentation under `docs/`.
- Keep `README.md` focused on the project introduction and documentation entry points.
- Give each rule or procedure one authoritative body. Link to it rather than copying it into other instruction files, local overrides, or guides. Keep references current when content moves.
- Create documentation when there is useful content to record. Do not create empty files or directories for completeness. Add task-specific guide routing only when those guides exist and are useful.

## Conditional Workflow Rules

### Task Worktrees

- Use a dedicated task worktree when parallel work, unrelated changes, or a long-running task needs isolation. Give concurrent code-producing tasks separate worktrees and explicit file ownership.
- Before editing in a task worktree, verify its directory, branch, and intended baseline using the repository's chosen workflow. Do not hard-code another project's branch names or merge procedure.
- A small, isolated change does not require a worktree when isolation adds no useful benefit.
- After task completion, clean up task-created worktrees and branches only after confirming their changes are integrated or safely preserved and no dependent task still needs them. Preserve unrelated work and artifacts.

### Independent Review

- Seek independent review before committing changes with significant correctness or maintenance risk when a suitable reviewer is available. Prioritize external interfaces, resource lifecycle, concurrency, authentication, and error recovery. Simple or routine documentation changes do not require this review.
- Give the reviewer the complete final diff, intended outcome, relevant context, validation results, and specific review questions. Keep the review scope bounded.
- Reuse completed validation evidence. Repeat checks when new changes, failures, or unresolved concerns justify it.
- Resolve blocking findings before committing and review subsequent substantive changes. If a reviewer is unavailable or returns no verdict, use appropriate self-review and disclose the review gap when material; do not describe the work as independently reviewed.
- Review establishes review status. Authority to merge, publish, or change a shared environment comes from the current task's authorization and applicable repository controls.

### Delivery and Handoff

- Summarize the outcome, relevant validation, and material limitations. Keep the handoff proportional to the task.
- During long tasks, report meaningful new progress, remaining work, and blockers. Identify any user action needed. Give an ETA only when it has a reasonable basis.
- When the task includes creating or updating a PR, verify its source and target, final diff, and stored description.
- When the task includes merging a PR, publishing a release, deploying, or another asynchronous remote operation, satisfy the applicable gates and monitor the corresponding operation and required workflows to a terminal state. Verify the intended branch, artifact, environment, or behavior as applicable.
- An accepted request, merged PR, or green aggregate workflow alone does not prove the requested delivery outcome. Check the actual result and report failed, canceled, skipped, or unverified required steps.
- Treat publication, deployment, and shared-environment changes as distinct operations within the authorized task scope. Permission to commit or push does not by itself authorize them. Reuse authorization already given; ask only when a proposed operation requires authority outside that scope.
