#!/bin/sh
# Explicit one-time billable infrastructure setup, using the pinned official CLI.
set -eu
umask 077
: "${CREATE_FIXTURE:?Set CREATE_FIXTURE=1 to create reusable infrastructure}"
test "$CREATE_FIXTURE" = 1
: "${ALIYUN_REGION:?}" "${ZONE_ID:?}" "${INSTANCE_TYPE:?}" "${IMAGE_ID:?}" "${SSH_PUBLIC_KEY_FILE:?}" "${MANAGER_CIDR:?Supply a narrow current Manager egress CIDR}"
: "${ACCEPTANCE_PROJECT:?Use the exact owner/repository}"
python3 - <<'CIDR'
import ipaddress, os
network = ipaddress.ip_network(os.environ["MANAGER_CIDR"], strict=True)
if network.version != 4 or network.prefixlen != 32 or "/" not in os.environ["MANAGER_CIDR"]:
    raise SystemExit("A single IPv4 /32 is required")
CIDR
aliyun version | grep -E '(^|[^0-9])3[.]5[.]2([^0-9]|$)' >/dev/null
mkdir -p .live
chmod 700 .live
if ! mkdir .live/fixture-setup.lock; then
  echo 'Another fixture setup owns the lock; inspect it before recovery.' >&2; exit 1
fi
trap 'rmdir .live/fixture-setup.lock' EXIT
trap 'exit 130' HUP INT TERM
for receipt in .live/vpc.json .live/vswitch.json .live/security-group.json .live/key.json .live/template.json .live/group.json .live/setup-id .live/setup-plan.json .live/ssh-rule.json .live/group-tags.json .live/group-enable.json; do
  if test -e "$receipt"; then echo "Existing fixture receipt is preserved: $receipt. Resume or tear down explicitly." >&2; exit 1; fi
done
public_key=$(cat "$SSH_PUBLIC_KEY_FILE")
case "$public_key" in ssh-rsa\ *) ;; *) echo 'The static fixture requires an OpenSSH RSA public key.' >&2; exit 1 ;; esac
test "$(printf '%s' "$public_key" | wc -l)" -eq 0
ssh-keygen -l -f "$SSH_PUBLIC_KEY_FILE" >/dev/null
setup_id=$(python3 -c 'import uuid; print(uuid.uuid4())')
printf '%s\n' "$setup_id" > .live/setup-id
fixture_name="fleeting-$(printf '%s' "$setup_id" | cut -c 1-12)"
SETUP_ID="$setup_id" FIXTURE_NAME="$fixture_name" python3 - <<'PLAN' > .live/setup-plan.json
import json, os
print(json.dumps({"setup_id": os.environ["SETUP_ID"], "resource_name": os.environ["FIXTURE_NAME"], "region": os.environ["ALIYUN_REGION"], "zone": os.environ["ZONE_ID"], "image": os.environ["IMAGE_ID"], "instance_type": os.environ["INSTANCE_TYPE"], "project": os.environ["ACCEPTANCE_PROJECT"]}, indent=2))
PLAN
script_directory=$(dirname "$0")
wait_vpc() { python3 "$script_directory/wait-available.py" vpc "$ALIYUN_REGION" "$vpc"; }
wait_vswitch() { python3 "$script_directory/wait-available.py" vswitch "$ALIYUN_REGION" "$vswitch"; }
# Record every accepted API outcome immediately. Keep this private ledger for teardown.
aliyun vpc CreateVpc --ClientToken "$setup_id-vpc" --RegionId "$ALIYUN_REGION" --CidrBlock 10.42.0.0/16 --VpcName "$fixture_name" > .live/vpc.json
vpc=$(python3 -c 'import json; print(json.load(open(".live/vpc.json"))["VpcId"])')
wait_vpc
aliyun vpc CreateVSwitch --ClientToken "$setup_id-vswitch" --RegionId "$ALIYUN_REGION" --VpcId "$vpc" --ZoneId "$ZONE_ID" --CidrBlock 10.42.1.0/24 --VSwitchName "$fixture_name" > .live/vswitch.json
vswitch=$(python3 -c 'import json; print(json.load(open(".live/vswitch.json"))["VSwitchId"])')
wait_vswitch
aliyun ecs CreateSecurityGroup --ClientToken "$setup_id-sg" --RegionId "$ALIYUN_REGION" --VpcId "$vpc" --SecurityGroupName "$fixture_name" > .live/security-group.json
sg=$(python3 -c 'import json; print(json.load(open(".live/security-group.json"))["SecurityGroupId"])')
aliyun ecs AuthorizeSecurityGroup --RegionId "$ALIYUN_REGION" --SecurityGroupId "$sg" --IpProtocol tcp --PortRange 22/22 --SourceCidrIp "$MANAGER_CIDR" --Description fleeting-fixture-manager > .live/ssh-rule.json
aliyun ecs ImportKeyPair --RegionId "$ALIYUN_REGION" --KeyPairName "$fixture_name" --PublicKeyBody "$public_key" > .live/key.json
aliyun ecs CreateLaunchTemplate --RegionId "$ALIYUN_REGION" --LaunchTemplateName "$fixture_name" --ImageId "$IMAGE_ID" --InstanceType "$INSTANCE_TYPE" --ZoneId "$ZONE_ID" --VSwitchId "$vswitch" --SecurityGroupId "$sg" --KeyPairName "$fixture_name" --InstanceChargeType PostPaid --SpotStrategy SpotAsPriceGo --InternetChargeType PayByTraffic --InternetMaxBandwidthOut 1 --SystemDisk.Category cloud_essd --SystemDisk.Size 20 --SystemDisk.DeleteWithInstance true --HttpTokens required --HttpEndpoint enabled > .live/template.json
lt=$(python3 -c 'import json; print(json.load(open(".live/template.json"))["LaunchTemplateId"])')
aliyun ess CreateScalingGroup --ClientToken "$setup_id-group" --RegionId "$ALIYUN_REGION" --ScalingGroupName "$fixture_name" --GroupType ECS --MinSize 0 --MaxSize 1 --DesiredCapacity 0 --LaunchTemplateId "$lt" --LaunchTemplateVersion 1 --VSwitchId "$vswitch" --ScalingPolicy release --MultiAZPolicy COST_OPTIMIZED --OnDemandBaseCapacity 0 --OnDemandPercentageAboveBaseCapacity 0 --CompensateWithOnDemand false --SpotInstanceRemedy false --AzBalance false --AutoRebalance false > .live/group.json
group=$(python3 -c 'import json; print(json.load(open(".live/group.json"))["ScalingGroupId"])')
aliyun ess TagResources --RegionId "$ALIYUN_REGION" --ResourceType scalinggroup --ResourceId.1 "$group" --Tag.1.Key fleeting-project --Tag.1.Value "$ACCEPTANCE_PROJECT" > .live/group-tags.json
aliyun ess EnableScalingGroup --RegionId "$ALIYUN_REGION" --ScalingGroupId "$group" > .live/group-enable.json
printf 'Prepared group %s in %s. Keep .live/*.json for explicit teardown.\n' "$group" "$ALIYUN_REGION"
