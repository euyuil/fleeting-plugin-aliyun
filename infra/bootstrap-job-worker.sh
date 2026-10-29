#!/bin/sh
# Job acceptance runs this prelude and bootstrap-linux.sh in one cloud-init shell.
set -eu
: "${PACKAGE_SNAPSHOT:?Pin the Ubuntu archive snapshot}" "${INSTALL_DOCKER:?Select the executor prerequisites}"
# shellcheck source=/dev/null
. /etc/os-release
test "$ID" = ubuntu
test "$VERSION_ID" = 26.04
test "$VERSION_CODENAME" = resolute
test "$(uname -m)" = x86_64
printf '%s\n' "$PACKAGE_SNAPSHOT" | LC_ALL=C grep -Eq '^[0-9]{8}T[0-9]{6}Z$'
case "$INSTALL_DOCKER" in true|false) ;; *) exit 1 ;; esac

# Use only the signed, fixed Ubuntu archive for this ephemeral worker's install.
install -d -m 755 /run/fleeting-apt
cat > /run/fleeting-apt/worker.sources <<EOF
Types: deb
URIs: https://archive.ubuntu.com/ubuntu
Suites: resolute resolute-updates resolute-security
Components: main universe
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
Snapshot: $PACKAGE_SNAPSHOT
EOF
cat > /run/fleeting-apt/apt.conf <<'EOF'
Dir::Etc::sourcelist "/run/fleeting-apt/worker.sources";
Dir::Etc::sourceparts "-";
DPkg::Lock::Timeout "180";
Acquire::Retries "3";
EOF
export APT_CONFIG=/run/fleeting-apt/apt.conf DEBIAN_FRONTEND=noninteractive
apt-get update
package_version() {
  version=$(apt-cache policy "$1" | awk '$1 == "Candidate:" {print $2; exit}')
  test -n "$version"
  test "$version" != '(none)'
  printf '%s\n' "$version"
}
GIT_PACKAGE_VERSION=$(package_version git)
CURL_PACKAGE_VERSION=$(package_version curl)
CA_PACKAGE_VERSION=$(package_version ca-certificates)
SSH_PACKAGE_VERSION=$(package_version openssh-server)
COREUTILS_PACKAGE_VERSION=$(package_version coreutils)
export GIT_PACKAGE_VERSION CURL_PACKAGE_VERSION CA_PACKAGE_VERSION SSH_PACKAGE_VERSION COREUTILS_PACKAGE_VERSION
if test "$INSTALL_DOCKER" = true; then
  docker_version=$(package_version docker.io)
  apt-get install -y --no-install-recommends "docker.io=$docker_version"
  systemctl enable --now docker
  docker info
fi
# bootstrap-linux.sh verifies the remaining tools before creating readiness.
