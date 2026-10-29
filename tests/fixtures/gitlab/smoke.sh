#!/bin/sh
set -eu
scenario=${FLEETING_JOB_SCENARIO:-smoke}
case "$scenario" in smoke|failure|cancel|timeout|capacity|drain) ;; *) exit 1 ;; esac
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
test ! -e /tmp/fleeting-acceptance-isolation
test ! -e acceptance-sentinel
printf '%s' "$FLEETING_NONCE" > /tmp/fleeting-acceptance-isolation
printf '%s' "$FLEETING_NONCE" > acceptance-sentinel
printf '{"nonce":"%s","fixture_sha":"%s","instance_id":"%s","runner_id":%s,"checkout_ok":true,"command_ok":true,"executor":"%s","container_detected":%s,"isolation_ok":true}\n' "$FLEETING_NONCE" "$CI_COMMIT_SHA" "$instance" "$CI_RUNNER_ID" "$FLEETING_EXECUTION_MODE" "$container" > result.json
printf 'FLEETING_CHECKPOINT %s %s %s %s\n' "$FLEETING_NONCE" "$scenario" "$CI_JOB_ID" "$instance"
case "$scenario" in
  capacity|drain)
    # A nonce-bound driver command releases this job after the queue assertions.
    elapsed=0
    until test -f "/tmp/fleeting-acceptance-release-$FLEETING_NONCE"; do
      test "$elapsed" -lt 1800 || exit 44
      sleep 1
      elapsed=$((elapsed + 1))
    done
    test "$(cat /tmp/fleeting-acceptance-isolation)" = "$FLEETING_NONCE"
    test "$(cat acceptance-sentinel)" = "$FLEETING_NONCE"
    ;;
  failure)
    sleep 30
    printf 'FLEETING_EXPECTED_EXIT %s 42\n' "$FLEETING_NONCE"
    exit 42
    ;;
  cancel|timeout)
    # Runner cancellation or RUNNER_SCRIPT_TIMEOUT must end this command.
    # Reaching the bounded fallback is a different failure, never acceptance.
    sleep 600
    exit 43
    ;;
esac
