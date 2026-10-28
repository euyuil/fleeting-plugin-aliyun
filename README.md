# GitLab Runner Autoscaling on Alibaba Cloud (Aliyun)

`fleeting-plugin-aliyun` is a community-maintained Fleeting plugin that scales
GitLab CI/CD workers using dedicated Alibaba Cloud (Aliyun) ECS Auto Scaling
(ESS) groups. Linux SSH workers and the Instance executor are the default. Jobs
run directly on VMs. The same provider supplies Docker Autoscaler and static
Windows WinRM HTTPS connection information.

The provider uses the official Fleeting protocol and Alibaba Cloud SDK. It
serializes capacity writes, persists operation intent, releases specified
instances, and separates cloud state from SSH readiness.

- [Setup and supported configurations](docs/operations.md)
- [Infrastructure and worker preparation](infra/README.md)
- [Pinned upstream parity and limitations](docs/upstream-parity.md)
- [Implementation and validation status](acceptance/status.json)
- [Acceptance report and remaining gates](docs/acceptance-report.md)
- [Testing, live setup, and cleanup](docs/testing.md)
- [Installation and distribution](docs/distribution.md)
- [Contributor commands](CONTRIBUTING.md)
- [Original specification](docs/specification.md)

Runner 19.5.1 is the initial minimum and primary baseline. Offline tests load the
real plugin process and execute authenticated local SSH commands. No live cloud
or GitLab validation has run, and no production acceptance is claimed.
Formal suspend/resume remains disabled; an isolated experiment and explicit live
entry points are provided. Native archives and OCI installation format have
offline checks; external release publication has not run. Actual ECS, disks,
images, and network traffic can incur charges.
