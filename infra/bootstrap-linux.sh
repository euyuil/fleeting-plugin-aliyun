#!/bin/sh
# Run as root on the selected, explicitly pinned Debian/Ubuntu image.
set -eu
fleeting_bootstrap_phase=4
: "${RUNNER_VERSION:?Set pinned version from upstream.lock.json}"
: "${RUNNER_SHA256:?Set pinned linux binary checksum from upstream.lock.json}"
: "${WORKER_PUBLIC_KEY:?Provide only the SSH public key}"
: "${GIT_PACKAGE_VERSION:?Pin the distribution package version}" "${CURL_PACKAGE_VERSION:?}" "${CA_PACKAGE_VERSION:?}" "${SSH_PACKAGE_VERSION:?}" "${COREUTILS_PACKAGE_VERSION:?}"
: "${WORKER_USER:=ci}"
printf '%s\n' "$WORKER_USER" | LC_ALL=C grep -Eq '^[a-z_][a-z0-9_-]{0,31}$'
case "$WORKER_PUBLIC_KEY" in ssh-rsa\ *|ssh-ed25519\ *|ecdsa-sha2-*\ *) ;; *) echo 'Provide one OpenSSH public key, never a private key.' >&2; exit 1 ;; esac
test "$(printf '%s' "$WORKER_PUBLIC_KEY" | wc -l)" -eq 0
case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; *) exit 1 ;; esac
test ! -L /var/lib/fleeting-worker
if test -e /var/lib/fleeting-worker; then
  test -d /var/lib/fleeting-worker
  test "$(stat -c %u /var/lib/fleeting-worker)" = 0
fi
install -d -m 755 /var/lib/fleeting-worker
for fleeting_state_file in ready failed phase; do
  test ! -L "/var/lib/fleeting-worker/$fleeting_state_file"
  if test -e "/var/lib/fleeting-worker/$fleeting_state_file"; then
    test -f "/var/lib/fleeting-worker/$fleeting_state_file"
    test "$(stat -c %u "/var/lib/fleeting-worker/$fleeting_state_file")" = 0
  fi
done
rm -f /var/lib/fleeting-worker/ready /var/lib/fleeting-worker/failed
fleeting_runner_tmp=
fleeting_worker_state() (
  trap - EXIT
  case "$1" in phase|failed) ;; *) exit 1 ;; esac
  fleeting_state_tmp=$(mktemp /var/lib/fleeting-worker/.state.XXXXXX) || exit 1
  trap 'rm -f "$fleeting_state_tmp"' EXIT
  if printf '%s\n' "$fleeting_bootstrap_phase" > "$fleeting_state_tmp" &&
      chmod 644 "$fleeting_state_tmp" && mv -f "$fleeting_state_tmp" "/var/lib/fleeting-worker/$1"; then
    exit 0
  fi
  rm -f "$fleeting_state_tmp" || :
  exit 1
)
fleeting_worker_state phase
fleeting_worker_finish() {
  fleeting_bootstrap_status=$?
  if test -n "$fleeting_runner_tmp"; then rm -f "$fleeting_runner_tmp" || :; fi
  if test "$fleeting_bootstrap_status" -ne 0; then
    fleeting_worker_state failed || :
  fi
  exit "$fleeting_bootstrap_status"
}
trap fleeting_worker_finish EXIT
trap 'exit 130' HUP INT TERM
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends "git=$GIT_PACKAGE_VERSION" "curl=$CURL_PACKAGE_VERSION" "ca-certificates=$CA_PACKAGE_VERSION" "openssh-server=$SSH_PACKAGE_VERSION" "coreutils=$COREUTILS_PACKAGE_VERSION"
fleeting_bootstrap_phase=5
fleeting_worker_state phase
printf '%s\n' "$WORKER_PUBLIC_KEY" | ssh-keygen -l -f /dev/stdin >/dev/null
id "$WORKER_USER" >/dev/null 2>&1 || useradd --create-home --shell /bin/sh "$WORKER_USER"
worker_home=$(getent passwd "$WORKER_USER" | awk -F: 'NR==1 {print $6}')
worker_group=$(id -gn "$WORKER_USER")
case "$worker_home" in /*) ;; *) echo 'Worker home is invalid' >&2; exit 1 ;; esac
test -d "$worker_home"
worker_home=$(cd "$worker_home" && pwd -P)
worker_uid=$(id -u "$WORKER_USER")
ssh_directory="$worker_home/.ssh"
if test -L "$ssh_directory"; then echo 'Unsafe worker SSH directory: symbolic link' >&2; exit 1; fi
test "$(stat -c %u "$worker_home")" = "$worker_uid"
if test -e "$ssh_directory"; then
  test -d "$ssh_directory"
  test "$(stat -c %u "$ssh_directory")" = "$worker_uid"
fi
authorized_keys="$ssh_directory/authorized_keys"
test ! -L "$authorized_keys"
if test -e "$authorized_keys"; then
  test -f "$authorized_keys"
  test "$(stat -c %u "$authorized_keys")" = "$worker_uid"
fi
install -d -m 700 -o "$WORKER_USER" -g "$worker_group" "$worker_home/.ssh"
if test ! -e "$authorized_keys"; then
  install -m 600 -o "$WORKER_USER" -g "$worker_group" /dev/null "$authorized_keys"
fi
if ! grep -Fqx -- "$WORKER_PUBLIC_KEY" "$authorized_keys"; then
  printf '\n%s\n' "$WORKER_PUBLIC_KEY" >> "$authorized_keys"
fi
chown "$WORKER_USER:$worker_group" "$authorized_keys"
chmod 600 "$authorized_keys"
fleeting_runner_tmp=$(mktemp "${TMPDIR:-/tmp}/fleeting-runner.XXXXXX")
fleeting_bootstrap_phase=6
fleeting_worker_state phase
curl --fail --silent --show-error --retry 3 --max-time 120 "https://gitlab-runner-downloads.s3.amazonaws.com/v$RUNNER_VERSION/binaries/gitlab-runner-linux-$arch" -o "$fleeting_runner_tmp"
fleeting_bootstrap_phase=7
fleeting_worker_state phase
printf '%s  %s\n' "$RUNNER_SHA256" "$fleeting_runner_tmp" | sha256sum -c -
install -m 755 "$fleeting_runner_tmp" /usr/local/bin/gitlab-runner
fleeting_bootstrap_phase=8
fleeting_worker_state phase
git --version
gitlab-runner --version
# Expand HOME in the target user's shell, after su changes identity.
# shellcheck disable=SC2016
timeout 10 su -s /bin/sh "$WORKER_USER" -c 'cd "$HOME" && test -w . && git --version && gitlab-runner --version'
install -d -m 755 /var/lib/fleeting-worker
touch /var/lib/fleeting-worker/ready
