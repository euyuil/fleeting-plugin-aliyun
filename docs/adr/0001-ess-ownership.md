# ADR 0001: Dedicated ESS groups

Status: accepted.

Use an existing Active ECS ESS group with desired capacity enabled, MinSize=0,
finite MaxSize, unit weights and a pinned launch template version. The Manager is
the sole capacity writer. Reject foreign attached instances and conflicting
rules, hooks, warm pools, standby/protection, proactive Spot replacement and
rebalancing. Do not enable disabled groups or repair their policy automatically.

The plugin owns capacity and member lifecycle inside region + group ID. It does
not create networking, groups or images. Explicit fixture tooling owns temporary
run markers and verifies tags and creation origin before cleanup. Restart reads
cloud membership; Runner state storage governs job recovery, which is not
promised across Manager loss.
