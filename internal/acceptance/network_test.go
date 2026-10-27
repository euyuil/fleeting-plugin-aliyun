package acceptance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
	"github.com/euyuil/fleeting-plugin-aliyun/internal/testutil"
)

type paginatedRules struct {
	fake                     *testutil.Fake
	owned                    bool
	revoked                  int
	badIdentity, repeatToken bool
}

func (p *paginatedRules) Call(ctx context.Context, service, action string, q map[string]string, out any) error {
	var response any
	switch action {
	case "DescribeLaunchTemplateVersions":
		response = map[string]any{"LaunchTemplateVersionSets": map[string]any{"LaunchTemplateVersionSet": []map[string]any{{"LaunchTemplateId": p.fake.Group.LaunchTemplateId, "VersionNumber": 1, "LaunchTemplateData": map[string]any{"SecurityGroupId": "sg-test"}}}}}
	case "DescribeSecurityGroupAttribute":
		next := "page-2"
		rules := []permission{}
		if q["NextToken"] == "page-2" {
			next = ""
			if p.repeatToken {
				next = "page-2"
			}
			if p.owned {
				rules = append(rules, permission{SecurityGroupRuleId: "sgr-owned", Description: "fleeting-100-1", SourceCidrIp: "192.0.2.1/32", IpProtocol: "tcp", PortRange: "22/22"})
			}
		}
		id := "sg-test"
		if p.badIdentity {
			id = "sg-foreign"
		}
		response = map[string]any{"SecurityGroupId": id, "RegionId": p.fake.Group.RegionId, "NextToken": next, "Permissions": map[string]any{"Permission": rules}}
	case "RevokeSecurityGroup":
		if q["SecurityGroupRuleId.1"] != "sgr-owned" {
			tpanic()
		}
		p.revoked++
		p.owned = false
		response = map[string]any{"RequestId": "test"}
	default:
		return p.fake.Call(ctx, service, action, q, out)
	}
	b, _ := json.Marshal(response)
	return json.Unmarshal(b, out)
}
func tpanic() { panic("unexpected rule ID") }
func TestCleanupFindsOwnedIngressOnSecondPage(t *testing.T) {
	f := testutil.New()
	api := &paginatedRules{fake: f, owned: true}
	c := &aliyun.Client{API: api, Region: f.Group.RegionId, GroupID: f.Group.ScalingGroupId}
	l := &Ledger{RunID: "100", Attempt: "1", SecurityGroupID: "sg-test"}
	if err := RemoveIngress(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	if api.owned || api.revoked != 1 {
		t.Fatal("owned second-page rule was missed")
	}
}
func TestRulePaginationRejectsWrongIdentityAndRepeatedToken(t *testing.T) {
	for _, repeat := range []bool{false, true} {
		f := testutil.New()
		api := &paginatedRules{fake: f, owned: true, badIdentity: !repeat, repeatToken: repeat}
		c := &aliyun.Client{API: api, Region: f.Group.RegionId, GroupID: f.Group.ScalingGroupId}
		if _, err := permissions(context.Background(), c, "sg-test"); err == nil {
			t.Fatal("invalid rule snapshot accepted")
		}
		if api.revoked != 0 {
			t.Fatal("invalid snapshot mutated rules")
		}
	}
}
