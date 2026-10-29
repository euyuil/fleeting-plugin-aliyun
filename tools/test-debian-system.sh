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
boot_pid=''
cleanup() {
  sudo machinectl terminate "$machine" >/dev/null 2>&1 || true
  if [[ -n "$boot_pid" ]]; then wait "$boot_pid" || true; fi
}
trap cleanup EXIT
sudo debootstrap --variant=minbase \
  --include=systemd-sysv,dbus,python3,openssh-client,dpkg-dev,util-linux,ca-certificates,adduser,passwd \
  bookworm "$root" https://deb.debian.org/debian
sudo systemd-nspawn --directory="$root" --machine="$machine" --boot --private-network \
  --bind-ro="$workspace:/workspace" > "$RUNNER_TEMP/debian-boot.log" 2>&1 &
boot_pid=$!
ready=false
for _ in {1..60}; do
  if sudo systemd-run --machine="$machine" --wait --pipe --quiet /bin/true 2>/dev/null; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  cat "$RUNNER_TEMP/debian-boot.log"
  exit 1
fi
sudo systemd-run --machine="$machine" --wait --pipe --collect --quiet \
  --working-directory=/workspace --setenv=PYTHONDONTWRITEBYTECODE=1 \
  /usr/bin/python3 tools/test-debian-lifecycle.py --disposable-host \
  --runner "/workspace/${runner#"$workspace/"}" --packages "/workspace/${packages#"$workspace/"}"
