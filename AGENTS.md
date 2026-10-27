# Repository Guidelines

This file defines repository-wide engineering rules for this open-source project.

## Language

- Write project documentation, code comments, commit messages, and PR titles and descriptions in English unless explicitly instructed otherwise.

## Project Awareness

- Before editing, read the applicable instructions and inspect the documentation, code, and development setup relevant to the task. Check the working directory, branch, and existing changes. Do not assume a technology stack.
- Follow existing module boundaries and conventions. Reuse existing implementations before introducing new abstractions.
- Use the project's documented tools and commands. Keep reusable development operations reproducible through scripts or documentation.

## PR Scope

- Each PR must focus on one clear purpose and deliver a complete, verifiable outcome. Include the implementation, tests, and documentation needed for that outcome.
- Keep each PR easy to review, independently validate, and revert. Split unrelated work and independently useful changes into separate PRs when they can be delivered separately.
- Keep tightly coupled changes together when splitting them would leave intermediate states unusable.
- There is no hard line-count limit. Judge PR scope by purpose, review complexity, validation effort, and rollback boundaries.
- Allow directly useful simplification, but do not expand a PR into a broader redesign or add dependencies or abstractions for hypothetical requirements.

## Commit and PR Workflow

- A clear user decision to implement a change authorizes the normal delivery workflow: create local commits, push the task branch, open a PR, and merge after all required CI checks and repository review requirements pass. Do not request separate confirmation for these steps. Analysis-only, planning-only, and review-only requests do not authorize implementation or delivery.
- Before a substantial change, identify a short sequence of coherent commits. Commit each completed, validated unit before starting the next independent change. Include the implementation, relevant tests, and documentation needed for that unit.
- Judge commit scope by purpose, reviewability, validation, and rollback boundaries. Keep tightly coupled changes together when splitting would leave an intermediate commit unusable. Do not use placeholder implementations to manufacture smaller commits.
- If unrelated purposes start accumulating in the working tree, stop adding code and separate the changes before continuing. Do not postpone all commits until the entire task is finished.
- For a single agent's sequential change chain, keep at most one active PR at a time. While that PR is pending, continue the next authorized change locally, including implementation, tests, and coherent commits, without waiting for the PR to finish. Keep the next change isolated in its own branch or worktree. Open its PR after the previous PR is merged or closed.
- Fix CI failures and resolve blocking review findings within the authorized scope. Do not bypass checks, required reviews, or branch protections to merge. Verify the PR's final source, target, diff, and stored description, and monitor merge and required workflows to their actual terminal results.
- Release publication, deployment, paid-resource operations, and changes to shared environments remain separate authorization boundaries. Ordinary code delivery does not authorize them automatically.

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

## Branch Names and Attribution

- Name feature branches `<type>/<short-kebab-case-description>`, such as `feat/ess-provider`, `fix/capacity-reconciliation`, or `docs/implementation-spec`. Use lowercase English names that describe the work.
- Do not include the name or branding of the coding agent used for the work in branch names, commit messages (including trailers), PR titles or descriptions, or commit author / committer metadata. Do not add coding-agent signatures or co-author attribution.

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
- After a task branch is merged and has no remaining work, delete both its remote and local branches and remove its task-created worktree. First verify that all changes are integrated or safely preserved, no needed working-tree content remains, and no dependent task still needs the branch or worktree. For squash or rebase merges, verify integration through the PR result and resulting content rather than relying only on commit ancestry. Preserve unrelated work and artifacts.

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
