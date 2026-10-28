#!/bin/sh
set -eu
test "$(git rev-parse HEAD)" = "$CI_COMMIT_SHA"
test -f .gitlab-ci.yml
case "$FLEETING_EXECUTION_MODE" in
  instance) test ! -f /.dockerenv; container=false ;;
  docker-autoscaler) test -f /.dockerenv; container=true ;;
  *) exit 1 ;;
esac
test "$(printf fixture-command | sha256sum | cut -d ' ' -f 1)" = "9abf5995c8f9ae752a79d889bdae20c821f1493e5d2743b0bc41f4058930a871"
token=$(curl --fail --silent --show-error --max-time 5 -X PUT -H 'X-aliyun-ecs-metadata-token-ttl-seconds: 60' http://100.100.100.200/latest/api/token)
instance=$(curl --fail --silent --show-error --max-time 5 -H "X-aliyun-ecs-metadata-token: $token" http://100.100.100.200/latest/meta-data/instance-id)
unset token
# Leave a bounded observation window before max_use_count=1 can release the VM.
sleep 10
case "$instance" in i-[a-zA-Z0-9]*) ;; *) exit 1 ;; esac
case "$FLEETING_NONCE" in *[!0-9a-f]*|'') exit 1 ;; esac
case "$CI_COMMIT_SHA" in *[!0-9a-f]*|'') exit 1 ;; esac
printf '{"nonce":"%s","fixture_sha":"%s","instance_id":"%s","runner_id":%s,"checkout_ok":true,"command_ok":true,"executor":"%s","container_detected":%s}\n' "$FLEETING_NONCE" "$CI_COMMIT_SHA" "$instance" "$CI_RUNNER_ID" "$FLEETING_EXECUTION_MODE" "$container" > result.json
