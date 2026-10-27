# Upstream baseline and capability parity

Reviewed on 2026-10-27. Exact versions are authoritative in
[`upstream.lock.json`](../upstream.lock.json). Runner 19.5.1 is both the initial
minimum and primary version; older versions are not claimed. Its `go.mod` pins
Fleeting `6ed75a33d303`, including all nine provider methods and the
`suspend_resume` capability. Newer official plugin module dependencies are not
silently substituted into the Runner protocol baseline.

Sources inspected before implementation:

- [Runner 19.5.1 go.mod](https://gitlab.com/gitlab-org/gitlab-runner/-/blob/v19.5.1/go.mod).
- [Fleeting provider interface](https://gitlab.com/gitlab-org/fleeting/fleeting/-/blob/6ed75a33d303/provider/interface.go), plugin bootstrap, connector and integration sources at the same commit.
- [AWS v1.3.1](https://gitlab.com/gitlab-org/fleeting/plugins/aws/-/tree/v1.3.1): provider, state manager, SSH/Windows, Spot and suspend paths.
- [Google Cloud v1.3.1](https://gitlab.com/gitlab-org/fleeting/plugins/googlecloud/-/tree/v1.3.1): managed group, metadata SSH, serial-port Windows credentials and suspend labels.
- [Azure v1.2.0](https://gitlab.com/gitlab-org/fleeting/plugins/azure/-/tree/v1.2.0): uniform VMSS, static connection information, instance-state mapping.
- [Official Fleeting documentation](https://docs.gitlab.com/runner/fleet_scaling/fleeting/) identifies `googlecloud` as the current repository.
- [Alibaba OpenAPI SDK v2.2.4](https://github.com/aliyun/darabonba-openapi/tree/master/golang) supplies signing, credential integration and context-aware API calls. The adapter uses typed local responses for the small ESS/ECS API surface, rather than compiling entire generated service catalogs.
- [Credentials v1.4.14](https://github.com/aliyun/credentials-go/tree/v1.4.14) supplies the standard refreshable credential abstraction. Environment STS credentials do not refresh themselves.

## Capability mapping

Implementation, offline validation, and live validation are maintained separately
in [`acceptance/status.json`](../acceptance/status.json). This snapshot delivers
the SDK, core provider, and opt-in expiring SSH credentials. Live acceptance tooling,
and experimental suspend are separate delivery steps. Native/OCI packaging has
offline build and official-installer validation; publication has not run.
No cloud or GitLab live validation is claimed.

| Capability | Official baseline behavior | Alibaba mapping / implementation boundary |
|---|---|---|
| Provider protocol, version, shutdown | All three use Fleeting bootstrap | Official nine-method interface and real-process host test |
| Zero/batch expansion | ASG desired / managed group resize / VMSS capacity | ESS absolute desired target, serialized mutations |
| Specified deletion | ASG/EC2 terminate / named delete / VMSS delete | ESS RemoveInstances, release=true semantics, <=20 IDs |
| Restart and pagination | Rediscover cloud membership | Full ESS and batched ECS snapshots, no local-only membership |
| Eventual consistency | Provider-specific cached intent and operations | Confirm accepted targets; fail closed on ambiguous operations |
| Spot and failure | AWS notice / GCP preemptibility / VMSS priority | ESS policy plus actual ECS SpotStrategy and disappearance |
| On-demand/mixed | Cloud launch configuration | Explicit production group configuration, strict live Spot preflight |
| Public/private and refreshed addresses | Cloud network interfaces | ECS addresses returned together, host selects route |
| Linux static SSH | All three | Runner connector_config; no worker registration |
| OS, architecture, user, port | Cloud inference plus connector overrides | Explicit settings first; ECS metadata fallback; reject unknown architectures |
| Instance executor | Official host | Default configuration and one-job live driver |
| Docker Autoscaler | Official host | Same provider, separate prepared Docker image |
| Windows/WinRM | AWS decrypted / GCP generated / Azure configured credentials | Static WinRM HTTPS; the pinned connector skips server certificate verification (ADR 0003) |
| Dynamic SSH | AWS Instance Connect / GCP metadata / Azure static path | Investigate Alibaba Cloud Assistant public API; ADR records timing and limitations |
| Heartbeat | Cloud-specific health, Azure currently no-op | Refresh ECS/ESS, distinguish API errors and failed instances |
| Suspend/resume | AWS standby+stop, GCP stop+labels, Azure no declared support | Standard unsupported error until ESS standby+stop is proven; see ADR |
| Credentials/STS | Each official SDK chain | Official Alibaba credentials abstraction, no keys in provider config |
| Native/OCI | Native binaries and Fleeting bootstrap container | Linux amd64/arm64, upstream installer-compatible OCI layout |

## Licenses

Fleeting and all three reference plugins are MIT. Alibaba OpenAPI and credentials
are Apache-2.0. Implementations are original; upstream APIs are imported, not
vendored. Binary distributions must include dependency licenses and notices.
Record the linked dependency inventory and third-party obligations when the
implementation and binary distribution are delivered.
