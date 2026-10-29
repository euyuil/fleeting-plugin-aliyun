#!/bin/sh
# Run as root on the selected, explicitly pinned Debian/Ubuntu image.
set -eu
: "${RUNNER_VERSION:?Set pinned version from upstream.lock.json}"
: "${RUNNER_SHA256:?Set pinned linux binary checksum from upstream.lock.json}"
: "${WORKER_PUBLIC_KEY:?Provide only the SSH public key}"
: "${GIT_PACKAGE_VERSION:?Pin the distribution package version}" "${CURL_PACKAGE_VERSION:?}" "${CA_PACKAGE_VERSION:?}" "${SSH_PACKAGE_VERSION:?}" "${COREUTILS_PACKAGE_VERSION:?}"
: "${WORKER_USER:=ci}"
printf '%s\n' "$WORKER_USER" | LC_ALL=C grep -Eq '^[a-z_][a-z0-9_-]{0,31}$'
case "$WORKER_PUBLIC_KEY" in ssh-rsa\ *|ssh-ed25519\ *|ecdsa-sha2-*\ *) ;; *) echo 'Provide one OpenSSH public key, never a private key.' >&2; exit 1 ;; esac
test "$(printf '%s' "$WORKER_PUBLIC_KEY" | wc -l)" -eq 0
case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; *) exit 1 ;; esac
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends "git=$GIT_PACKAGE_VERSION" "curl=$CURL_PACKAGE_VERSION" "ca-certificates=$CA_PACKAGE_VERSION" "openssh-server=$SSH_PACKAGE_VERSION" "coreutils=$COREUTILS_PACKAGE_VERSION"
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
runner_tmp=$(mktemp "${TMPDIR:-/tmp}/fleeting-runner.XXXXXX")
trap 'rm -f "$runner_tmp"' EXIT
trap 'exit 130' HUP INT TERM
curl --fail --silent --show-error --retry 3 --max-time 120 "https://gitlab-runner-downloads.s3.amazonaws.com/v$RUNNER_VERSION/binaries/gitlab-runner-linux-$arch" -o "$runner_tmp"
printf '%s  %s\n' "$RUNNER_SHA256" "$runner_tmp" | sha256sum -c -
install -m 755 "$runner_tmp" /usr/local/bin/gitlab-runner
git --version
gitlab-runner --version
# Expand HOME in the target user's shell, after su changes identity.
# shellcheck disable=SC2016
timeout 10 su -s /bin/sh "$WORKER_USER" -c 'cd "$HOME" && test -w . && git --version && gitlab-runner --version'
install -d -m 755 /var/lib/fleeting-worker
touch /var/lib/fleeting-worker/ready
