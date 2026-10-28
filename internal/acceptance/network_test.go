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
	authorized               int
	badIdentity, repeatToken bool
	protocol, source         string
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
				protocol, source := p.protocol, p.source
				if protocol == "" {
					protocol = "tcp"
				}
				if source == "" {
					source = "192.0.2.1/32"
				}
				rules = append(rules, permission{SecurityGroupRuleId: "sgr-owned", Description: "fleeting-100-1", SourceCidrIp: source, IpProtocol: protocol, PortRange: "22/22"})
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
	case "AuthorizeSecurityGroup":
		p.authorized++
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
	l := &Ledger{RunID: "100", Attempt: "1", SecurityGroupID: "sg-test", IngressCIDR: "192.0.2.1/32"}
	if err := RemoveIngress(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	if api.owned || api.revoked != 1 {
		t.Fatal("owned second-page rule was missed")
	}
}

func TestCleanupAcceptsProtocolCaseButRejectsDifferentOwnedRuleScope(t *testing.T) {
	for _, source := range []string{"192.0.2.1/32", "192.0.2.2/32", "192.0.2.0/24"} {
		f := testutil.New()
		api := &paginatedRules{fake: f, owned: true, protocol: "TCP", source: source}
		c := &aliyun.Client{API: api, Region: f.Group.RegionId, GroupID: f.Group.ScalingGroupId}
		l := &Ledger{RunID: "100", Attempt: "1", SecurityGroupID: "sg-test", IngressCIDR: "192.0.2.1/32"}
		err := RemoveIngress(context.Background(), c, l)
		if source == l.IngressCIDR {
			if err != nil || api.revoked != 1 {
				t.Fatal("canonical TCP rule was not removed", err)
			}
		} else if err == nil || api.revoked != 0 {
			t.Fatal("different source was removed", source, err)
		}
	}
}

func TestExistingUppercaseTCPRuleIsReused(t *testing.T) {
	f := testutil.New()
	api := &paginatedRules{fake: f, owned: true, protocol: "TCP"}
	c := &aliyun.Client{API: api, Region: f.Group.RegionId, GroupID: f.Group.ScalingGroupId}
	l := &Ledger{RunID: "100", Attempt: "1"}
	if err := EnsureIngress(context.Background(), c, l, "192.0.2.1/32"); err != nil {
		t.Fatal(err)
	}
	if api.authorized != 0 {
		t.Fatal("equivalent uppercase TCP rule was duplicated")
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
