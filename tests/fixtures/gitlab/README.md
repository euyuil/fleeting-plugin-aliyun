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

Use a dedicated project-scoped PAT with the permissions in
[release job acceptance](../../../docs/job-acceptance.md#dedicated-configuration). GitLab.com Free does not require a project access token. CE may use a
project token where its version and policy permit. Set the project's maximum
job timeout and API-variable restrictions for the nonce, expected ID,
dedicated tag, `FLEETING_EXECUTION_MODE` and fixed `FLEETING_JOB_SCENARIO` inputs shown in the fixture. Pin the
reviewed fixture SHA after updates. Keep artifacts for one day.

Set `GITLAB_URL=https://gitlab.com`, or the HTTPS URL of the existing CE server.
Both GitHub and workers must reach checkout, job and artifact endpoints. A
`clone_url` override alone is insufficient. The selected executor runs the job
on the ECS VM directly or inside its Docker container. The fixture does not
build the plugin or run its unit tests; only Docker executor jobs need Docker.

The release gate uses the same two reviewed files for successful jobs and
expected `failure`, `cancel`, and `timeout` scenarios. An absent scenario defaults
to `smoke` for existing callers. The three-minute script timeout is fixed in
`.gitlab-ci.yml`; cancellation is triggered by the driver only after the script
checkpoint. The API token must permit reading job traces as well as job metadata
and canceling pipelines. Keep logs and IDs private. See the authoritative
[scenario contract](../../../docs/job-acceptance.md#expected-failures-and-continued-service)
for outcome, identity, release and follow-up-job requirements.
