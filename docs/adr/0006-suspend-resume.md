# ADR 0006: Keep formal suspend disabled; provide an isolated experiment

Status: production capability disabled; experimental controller implemented.

The production provider declares no suspend_resume capability and both required
methods return the upstream standard unsupported error. A method returning nil
would falsely simulate support. Formal promotion requires target-configuration
live evidence, including resumed SSH usability. Production admission continues
to reject standby/recycle pools. The experiment checks every standby member
against its exact target set before allowing those capacity counters; all other
production policy checks remain active. Inconsistent read snapshots require a
bounded retry and do not authorize another capacity write.

The isolated `internal/experimental` controller requires an exclusively claimed
`suspend` test fixture with auto-created members. It journals explicit IDs and intent,
enters ESS standby before ECS StopInstance(StopCharging), and resumes by starting
the same instance before ExitStandby. Reconcile advances asynchronous stages;
there is no background controller. Standby is counted in ESS steady-state
capacity, so desired must stay unchanged. It prevents health replacement for a
stopped member and preserves Fleeting's maximum identity count. An unexpected
capacity change stops the experiment. Existing IDs and cloud state plus the
durable journal recover after a process restart. Failed start due to unavailable
capacity retains standby identity/disks and returns an error for retry.

Normal stopping, economical stopping, memory hibernation and ESS process
suspension are distinct. StopCharging can release compute/public IP while disks
remain billed; unsupported machine types can fall back to ordinary stopping.
The experimental live suite must verify the actual target configuration before
formal support is claimed. No promise that Spot cannot stop is made. Memory
hibernation in ESS groups is a documented platform restriction; SuspendProcesses
is not VM suspension.

The experimental package is not linked into the production plugin. Its fake API
tests cover state transitions, desired preservation, unaffected members, restart
and capacity-unavailable resume. The separate tagged extended live suspend test must check fresh SSH after resumption. Formal provider capability activation is deliberately
blocked by missing live evidence, rather than hidden as completed parity.
Interrupted/deleted instances fail the experiment; no second replacement request
is invented. Cleanup may explicitly remove the same owned standby members after
zeroing intent, without another desired decrement.

Sources reviewed 2026-10-27:

- [ESS steady-state/desired capacity](https://help.aliyun.com/zh/auto-scaling/user-guide/expected-number-of-instances).
- [EnterStandby](https://help.aliyun.com/zh/auto-scaling/developer-reference/api-ess-2014-08-28-enterstandby).
- [Economical mode including Spot](https://help.aliyun.com/zh/ecs/user-guide/economical-mode).
- [Hibernation limitations](https://help.aliyun.com/zh/ecs/user-guide/hibernate-an-instance).
