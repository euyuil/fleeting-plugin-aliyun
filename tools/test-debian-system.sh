#!/usr/bin/env bash
# Boot an isolated Debian 12 userspace; never run the lifecycle test on the host.
set -euo pipefail
if [[ "${RUNNER_ENVIRONMENT:-}" != github-hosted || "$(uname -m)" != x86_64 ]]; then
  echo 'requires a disposable GitHub-hosted Linux amd64 runner' >&2
  exit 1
fi
packages=$(realpath "${1:?candidate distribution directory required}")
runner=$(realpath "${2:?checksum-verified Runner path required}")
workspace=$(pwd -P)
[[ "$packages" == "$workspace/"* && "$runner" == "$workspace/"* ]]
root=$(mktemp -d "${RUNNER_TEMP:?}/debian-system.XXXXXX")
machine="fleeting-debian-$$"
unit="$machine.service"
boot_log="$RUNNER_TEMP/$machine.log"
cleanup() {
  status=$?
  trap - EXIT
  sudo timeout 30s systemctl stop "$unit" >/dev/null 2>&1 || true
  if [[ "$status" != 0 && -f "$boot_log" ]]; then sudo tail -n 120 "$boot_log"; fi
  exit "$status"
}
trap cleanup EXIT
sudo debootstrap --variant=minbase \
  --include=systemd-sysv,dbus,python3,openssh-client,dpkg-dev,util-linux,ca-certificates,adduser,passwd \
  bookworm "$root" https://deb.debian.org/debian
printf 'Starting isolated Debian systemd machine %s\n' "$machine"
sudo systemd-run --unit="$unit" --collect --property=Type=exec \
  --property=KillMode=mixed --property=TimeoutStopSec=15s --property=RuntimeMaxSec=10min \
  --property=Delegate=yes --property=DelegateSubgroup=supervisor \
  --property="StandardOutput=append:$boot_log" --property="StandardError=append:$boot_log" \
  systemd-nspawn --keep-unit --directory="$root" --machine="$machine" --boot --private-network \
  --console=read-only --bind-ro="$workspace:/workspace"
ready=false
for _ in {1..12}; do
  if sudo timeout 5s systemd-run --machine="$machine" --wait --pipe --quiet /bin/true 2>/dev/null; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  echo 'Debian systemd did not become ready within the bounded wait' >&2
  exit 1
fi
printf 'Running candidate package lifecycle inside Debian 12\n'
sudo timeout 5m systemd-run --machine="$machine" --wait --pipe --collect --quiet \
  --working-directory=/workspace --setenv=PYTHONDONTWRITEBYTECODE=1 \
  /usr/bin/python3 tools/test-debian-lifecycle.py --disposable-host \
  --runner "/workspace/${runner#"$workspace/"}" --packages "/workspace/${packages#"$workspace/"}"
