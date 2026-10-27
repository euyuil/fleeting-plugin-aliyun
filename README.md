# Fleeting Plugin for Alibaba Cloud

A GitLab Runner Fleeting provider for dedicated Alibaba Cloud ECS Auto Scaling
(ESS) groups. Linux SSH workers and the Instance executor are the default. Jobs
run directly on VMs. The same provider supplies Docker Autoscaler and static
Windows WinRM HTTPS connection information.

The provider uses the official Fleeting protocol and Alibaba Cloud SDK. It
serializes capacity writes, persists operation intent, releases specified
instances, and separates cloud state from SSH readiness.

- [Setup and supported configurations](docs/operations.md)
- [Pinned upstream parity and limitations](docs/upstream-parity.md)
- [Implementation and validation status](acceptance/status.json)
- [Installation and distribution](docs/distribution.md)
- [Contributor commands](CONTRIBUTING.md)
- [Original specification](docs/specification.md)

Runner 19.5.1 is the initial minimum and primary baseline. Offline tests load the
real plugin process and execute authenticated local SSH commands. No live cloud
or GitLab validation has run, and no production acceptance is claimed.
Suspend/resume remains disabled. Live tooling follows separately. Native archives and OCI installation format
have offline checks; external release publication has not run. Actual ECS, disks, images, and network traffic can incur charges.
