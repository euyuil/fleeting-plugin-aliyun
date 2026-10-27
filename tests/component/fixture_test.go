package component

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestFixtureSetupWritesEmptyStrictSpotGroupAndPreservesInterruptedPlan(t *testing.T) {
	for _, lostReply := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "lost-reply"}[lostReply], func(t *testing.T) {
			root := t.TempDir()
			private, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
			public, err := ssh.NewPublicKey(&private.PublicKey)
			if err != nil {
				t.Fatal(err)
			}
			keyFile := filepath.Join(root, "worker.pub")
			if err = os.WriteFile(keyFile, ssh.MarshalAuthorizedKey(public), 0600); err != nil {
				t.Fatal(err)
			}
			python, err := exec.LookPath("python3")
			if err != nil {
				t.Fatal(err)
			}
			stub := `import json,os,sys
if sys.argv[1]=='version': print('3.5.2'); sys.exit(0)
action=sys.argv[2]
query=dict(zip(sys.argv[3::2],sys.argv[4::2]))
with open(os.environ['TEST_CALLS'],'a') as log: log.write(json.dumps({'action':action,'query':query})+'\n')
if action==os.environ.get('TEST_LOST_REPLY'): sys.exit(99)
responses={
 'CreateVpc':{'VpcId':'vpc-test'},
 'DescribeVpcs':{'Vpcs':{'Vpc':[{'Status':'Available','VpcId':'vpc-test'}]}},
 'CreateVSwitch':{'VSwitchId':'vsw-test'},
 'DescribeVSwitches':{'VSwitches':{'VSwitch':[{'Status':'Available','VSwitchId':'vsw-test'}]}},
 'CreateSecurityGroup':{'SecurityGroupId':'sg-test'},
 'AuthorizeSecurityGroup':{'RequestId':'test'},
 'ImportKeyPair':{'KeyPairName':query.get('--KeyPairName')},
 'CreateLaunchTemplate':{'LaunchTemplateId':'lt-test'},
 'CreateScalingGroup':{'ScalingGroupId':'asg-test'},
 'TagResources':{'RequestId':'test'},
 'EnableScalingGroup':{'RequestId':'test'},
}
if action not in responses: sys.exit(98)
print(json.dumps(responses[action]))
`
			if err = os.WriteFile(filepath.Join(root, "aliyun"), []byte("#!"+python+"\n"+stub), 0700); err != nil {
				t.Fatal(err)
			}
			script, err := filepath.Abs("../../infra/prepare.sh")
			if err != nil {
				t.Fatal(err)
			}
			callFile := filepath.Join(root, "calls.jsonl")
			env := append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "CREATE_FIXTURE=1", "ALIYUN_REGION=cn-test-1", "ZONE_ID=cn-test-1a", "INSTANCE_TYPE=ecs.test.small", "IMAGE_ID=image-test", "SSH_PUBLIC_KEY_FILE="+keyFile, "MANAGER_CIDR=192.0.2.1/32", "ACCEPTANCE_PROJECT=owner/repo", "TEST_CALLS="+callFile)
			if lostReply {
				env = append(env, "TEST_LOST_REPLY=CreateVpc")
			}
			run := func() error {
				cmd := exec.Command("sh", script)
				cmd.Dir = root
				cmd.Env = env
				output, e := cmd.CombinedOutput()
				if e != nil {
					t.Log(string(output))
				}
				return e
			}
			if err = run(); (err != nil) != lostReply {
				t.Fatal(err)
			}
			before, err := os.ReadFile(callFile)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(filepath.Join(root, ".live", "setup-plan.json")); err != nil {
				t.Fatal("setup plan missing", err)
			}
			if !lostReply {
				seenGroup, seenTemplate := false, false
				for _, line := range strings.Split(strings.TrimSpace(string(before)), "\n") {
					var call struct {
						Action string
						Query  map[string]string
					}
					if err = json.Unmarshal([]byte(line), &call); err != nil {
						t.Fatal(err)
					}
					if call.Action == "CreateScalingGroup" {
						seenGroup = true
						for key, value := range map[string]string{"--MinSize": "0", "--MaxSize": "1", "--DesiredCapacity": "0", "--CompensateWithOnDemand": "false"} {
							if call.Query[key] != value {
								t.Fatal("unsafe group", call.Query)
							}
						}
					}
					if call.Action == "CreateLaunchTemplate" {
						seenTemplate = true
						if call.Query["--SpotStrategy"] != "SpotAsPriceGo" || call.Query["--SystemDisk.DeleteWithInstance"] != "true" {
							t.Fatal("unsafe template", call.Query)
						}
					}
				}
				if !seenGroup || !seenTemplate {
					t.Fatal("fixture incomplete")
				}
			}
			if err = run(); err == nil {
				t.Fatal("second setup overwrote receipts")
			}
			after, err := os.ReadFile(callFile)
			if err != nil || string(before) != string(after) {
				t.Fatal("second setup issued cloud calls", err)
			}
		})
	}
}
