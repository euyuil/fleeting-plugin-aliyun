# Fixed GitLab fixture

Use a fixed GitLab.com Free project or an existing reachable GitLab CE project.
Copy `.gitlab-ci.yml` and `smoke.sh` from this directory. Commit both files, then
record the full commit SHA in `GITLAB_FIXTURE_SHA` and its controlled branch/tag
in `GITLAB_FIXTURE_REF`. The live driver checks the ref and job artifact against
that SHA. Review the two files when updating the SHA; do not allow untrusted
fixture changes or arbitrary command variables.

Create a **project runner**, using the new runner authentication-token workflow.
Select the dedicated tag used by `GITLAB_RUNNER_TAG`, disable untagged jobs, lock
the runner to this project, and disable shared/hosted runners for the project.
Save its reusable authentication token and numeric runner ID. Do not register
workers individually. The temporary GitHub Manager uses this one fixed runner;
cleanup never deletes that runner or its token. Remove stale manager records
through the runner UI if necessary, after confirming they are inactive.

Use a dedicated user's PAT with `api` scope and access only to the fixture
project. GitLab.com Free does not require a project access token. CE may use a
project token where its version and policy permit. Set the project's maximum
job timeout and API-variable restrictions for the nonce, expected ID,
dedicated tag and `FLEETING_EXECUTION_MODE` inputs shown in the fixture. Pin the
reviewed fixture SHA after updates. Keep artifacts for one day.

Set `GITLAB_URL=https://gitlab.com`, or the HTTPS URL of the existing CE server.
Both GitHub and workers must reach checkout, job and artifact endpoints. A
`clone_url` override alone is insufficient. The job runs on the ECS VM directly;
it does not build the plugin, run its unit tests or require Docker.
