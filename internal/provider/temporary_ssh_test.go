package provider_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
	provider "github.com/euyuil/fleeting-plugin-aliyun/internal/provider"
	"github.com/euyuil/fleeting-plugin-aliyun/internal/testutil"
	upstream "gitlab.com/gitlab-org/fleeting/fleeting/provider"
	"golang.org/x/crypto/ssh"
)

func TestTemporarySSHExpiresAndReacquires(t *testing.T) {
	f := testutil.New()
	f.Add("i-a")
	g := &provider.InstanceGroup{Region: f.Group.RegionId, ScalingGroupID: f.Group.ScalingGroupId, SSHCredentials: "cloud_assistant", Client: &aliyun.Client{API: f, Region: f.Group.RegionId, GroupID: f.Group.ScalingGroupId}}
	_, err := g.Init(context.Background(), nil, upstream.Settings{ConnectorConfig: upstream.ConnectorConfig{Username: "ci"}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := g.ConnectInfo(context.Background(), "i-a")
	if err != nil {
		t.Fatal(err)
	}
	if first.Expires == nil || time.Until(*first.Expires) < 20*time.Second || time.Until(*first.Expires) > 60*time.Second {
		t.Fatal("invalid expiry")
	}
	if _, err = ssh.ParsePrivateKey(first.Key); err != nil {
		t.Fatal(err)
	}
	second, err := g.ConnectInfo(context.Background(), "i-a")
	if err != nil || string(first.Key) == string(second.Key) {
		t.Fatal("key was not reacquired", err)
	}
	for _, c := range f.Calls {
		if c.Action == "RunCommand" {
			script, _ := base64.StdEncoding.DecodeString(c.Query["CommandContent"])
			parts := strings.Fields(string(script))
			if len(parts) != 2 || parts[0] != "/usr/local/bin/fleeting-authorize-key" {
				t.Fatal("unexpected helper command")
			}
			line, decodeErr := base64.StdEncoding.DecodeString(strings.Trim(parts[1], "'"))
			if decodeErr != nil || strings.Contains(string(line), "PRIVATE KEY") || c.Query["Username"] != "ci" || c.Query["WorkingDir"] != "/" || c.Query["KeepCommand"] != "false" {
				t.Fatal("unsafe Cloud Assistant command")
			}
			if _, _, _, _, err := ssh.ParseAuthorizedKey(line); err != nil {
				t.Fatal("payload is not an authorized public key", err)
			}
		}
	}
}

type invocationAPI struct {
	aliyun.API
	body string
}

func (a invocationAPI) Call(ctx context.Context, service, action string, q map[string]string, out any) error {
	if action == "DescribeInvocationResults" {
		return json.Unmarshal([]byte(a.body), out)
	}
	return a.API.Call(ctx, service, action, q, out)
}

func TestTemporarySSHRejectsUnverifiedInvocation(t *testing.T) {
	for _, body := range []string{
		`{"Invocation":{}}`,
		`{"Invocation":{"TotalCount":1,"InvocationResults":{"InvocationResult":[{"InstanceId":"i-a","InvokeId":"invoke-test","InvokeRecordStatus":"Finished","Output":"fleeting-key-ready"}]}}}`,
		`{"Invocation":{"TotalCount":1,"InvocationResults":{"InvocationResult":[{"InstanceId":"i-a","InvokeId":"invoke-test","InvokeRecordStatus":"Finished","ExitCode":null,"Output":"fleeting-key-ready"}]}}}`,
		`{"Invocation":{"TotalCount":1,"InvocationResults":{"InvocationResult":[{"InstanceId":"i-a","InvokeId":"invoke-test","InvokeRecordStatus":"Finished","ExitCode":0}]}}}`,
		`{"Invocation":{"TotalCount":-1}}`,
		`{"Invocation":{"TotalCount":2,"InvocationResults":{"InvocationResult":[{"InstanceId":"i-a","InvokeId":"invoke-test","InvokeRecordStatus":"Finished"},{"InstanceId":"i-a","InvokeId":"invoke-test","InvokeRecordStatus":"Finished"}]}}}`,
		`{"Invocation":{"TotalCount":1,"InvocationResults":{"InvocationResult":[{"InstanceId":"i-other","InvokeId":"invoke-test","InvokeRecordStatus":"Finished"}]}}}`,
		`{"Invocation":{"TotalCount":1,"InvocationResults":{"InvocationResult":[{"InstanceId":"i-a","InvokeId":"invoke-test","InvokeRecordStatus":"Finished","ExitCode":1}]}}}`,
	} {
		f := testutil.New()
		f.Add("i-a")
		g := &provider.InstanceGroup{Region: f.Group.RegionId, ScalingGroupID: f.Group.ScalingGroupId, SSHCredentials: "cloud_assistant", Client: &aliyun.Client{API: invocationAPI{f, body}, Region: f.Group.RegionId, GroupID: f.Group.ScalingGroupId}}
		if _, err := g.Init(context.Background(), nil, upstream.Settings{ConnectorConfig: upstream.ConnectorConfig{Username: "ci"}}); err != nil {
			t.Fatal(err)
		}
		info, err := g.ConnectInfo(context.Background(), "i-a")
		if err == nil || len(info.Key) != 0 {
			t.Fatal("unverified invocation returned credentials")
		}
	}
}
