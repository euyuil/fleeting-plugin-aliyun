# 0.1.0-rc.4

This prerelease targets GitLab Runner 19.5.1 and the pinned Fleeting interface.
It adds complete, source-bound acceptance for the selected Linux amd64 Instance
and Docker Autoscaler job paths. Use it for evaluation; production and broader
workload coverage are not claimed.

## Installation

Download the architecture-matching native archive or Debian package and
`SHA256SUMS` from this release. The Debian package version is `0.1.0~rc.4`;
GitHub normalizes the uploaded asset name to `0.1.0.rc.4`. Save the download under
its original tilde filename before checking `SHA256SUMS`.

The exact OCI version is `ghcr.io/euyuil/fleeting-plugin-aliyun:0.1.0-rc.4`.
GitLab Runner is installed separately. The package does not register a Runner,
create permanent cloud infrastructure, or start a Manager implicitly.

## Changes since 0.1.0-rc.3

- Complete real-job coverage for smoke, script failure, cancellation, timeout,
  capacity/queueing, drain, worker loss, Manager crash/recovery and published
  baseline upgrade on both supported executor paths.
- Exercise upgrade from the real published `0.1.0-rc.3` Debian package, including
  retained state and rollback checks before candidate validation succeeds.
- Run the verification harness as two independent executor lanes with distinct
  locked project Runners and serial cases within each lane. Capacity retains
  its original two-running/one-pending assertion.
- Preserve recovery journal and sanitized error evidence. Freeze normal-release
  observations before fallback cleanup, and distinguish inventory, removal and
  associated-resource phases and deadlines.
- Reject partial ESS membership snapshots; bounded whole-snapshot rereads cover
  specified inconsistent pagination observations without accepting a partial map.
- Align the existing worker readiness/acquisition setup and pinned bootstrap
  phases, and pace GitLab reads/control attempts without adding write retries
  or relaxing cloud budgets, TTLs, scenario deadlines or cleanup assertions.
- Verify the full candidate package lifecycle on native Ubuntu 24.04 and 26.04,
  each on amd64 and arm64; retain the isolated Debian 12/systemd check.

## Accepted source and publication verification

The initial full validation used source
`6d96f8ad7082c2db3c838c8d5db6d227d73927d3`, accepted by
[full run 41212022560/1](https://github.com/euyuil/fleeting-plugin-aliyun/actions/runs/41212022560):
18 real cases, six plugin scenarios, four native platforms and all final gates
passed. Exact independent cleanup confirmed zero leftovers. The selected source,
manifest and accepted run are recorded below for each publication. Reuse copies
the accepted distribution bytes without rebuilding; the default fresh-candidate
path requires its own full acceptance before publishing.

Publication is followed by anonymous asset/OCI installation on the four native
Ubuntu platforms and the two public-package executor smokes. Their receipts
must match the sealed candidate before release completion succeeds.

The [acceptance report](https://github.com/euyuil/fleeting-plugin-aliyun/blob/main/docs/acceptance-report.md)
retains earlier failures and unknown historical causes. Current success does not
identify missing historical cloud responses or rate-limit headers. Native arm64
package verification does not establish real arm64 cloud jobs; Windows, Darwin,
production and arbitrary workloads remain outside this result. Suspend/resume
remains disabled. Use the documented isolated, trusted connection route.

Earlier immutable release versions and assets remain unchanged.
