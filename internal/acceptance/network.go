package acceptance

import (
	"context"
	"errors"
	"net"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

func ruleDescription(l *Ledger) string { return "fleeting-" + l.RunID + "-" + l.Attempt }

type permission struct{ SecurityGroupRuleId, Description, IpProtocol, PortRange, SourceCidrIp string }

func permissions(ctx context.Context, c *aliyun.Client, sg string) ([]permission, error) {
	var all []permission
	next := ""
	seen := map[string]bool{}
	for {
		var out struct {
			SecurityGroupId, RegionId, NextToken string
			Permissions                          *struct{ Permission []permission }
		}
		if err := c.API.Call(ctx, "ecs", "DescribeSecurityGroupAttribute", map[string]string{"RegionId": c.Region, "SecurityGroupId": sg, "Direction": "ingress", "MaxResults": "1000", "NextToken": next}, &out); err != nil {
			return nil, err
		}
		if out.SecurityGroupId != sg || out.RegionId != c.Region || out.Permissions == nil {
			return nil, errors.New("security group response identity or permissions missing")
		}
		all = append(all, out.Permissions.Permission...)
		if out.NextToken == "" {
			return all, nil
		}
		if seen[out.NextToken] {
			return nil, errors.New("repeated security-group pagination token")
		}
		seen[out.NextToken] = true
		next = out.NextToken
	}
}

func EnsureIngress(ctx context.Context, c *aliyun.Client, l *Ledger, cidr string) error {
	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return errors.New("Manager egress must be an IPv4 /32")
	}
	ones, bits := network.Mask.Size()
	if bits != 32 || ones != 32 || ip.To4() == nil {
		return errors.New("Manager egress must be an IPv4 /32")
	}
	g, err := c.Group(ctx)
	if err != nil {
		return err
	}
	tmpl, err := c.Template(ctx, g)
	if err != nil {
		return err
	}
	sg := tmpl.LaunchTemplateData.SecurityGroupId
	if sg == "" {
		return errors.New("smoke requires one explicit launch-template security group")
	}
	l.SecurityGroupID = sg
	l.IngressCIDR = network.String()
	if err = l.Save(); err != nil {
		return err
	}
	existing, err := permissions(ctx, c, sg)
	if err != nil {
		return err
	}
	for _, p := range existing {
		if p.SourceCidrIp == l.IngressCIDR && p.IpProtocol == "tcp" && p.PortRange == "22/22" {
			return nil
		}
	}
	var out struct{ RequestId string }
	return c.API.Call(ctx, "ecs", "AuthorizeSecurityGroup", map[string]string{"RegionId": c.Region, "SecurityGroupId": sg, "IpProtocol": "tcp", "PortRange": "22/22", "SourceCidrIp": l.IngressCIDR, "Description": ruleDescription(l)}, &out)
}
func RemoveIngress(ctx context.Context, c *aliyun.Client, l *Ledger) error {
	g, err := c.Group(ctx)
	if err != nil {
		return err
	}
	tmpl, err := c.Template(ctx, g)
	if err != nil {
		return err
	}
	sg := tmpl.LaunchTemplateData.SecurityGroupId
	if l.SecurityGroupID != "" && sg != l.SecurityGroupID {
		return errors.New("security group changed during the run")
	}
	if sg == "" {
		return nil
	}
	rules, err := permissions(ctx, c, sg)
	if err != nil {
		return err
	}
	for _, p := range rules {
		if p.Description != ruleDescription(l) {
			continue
		}
		_, network, e := net.ParseCIDR(p.SourceCidrIp)
		if e != nil {
			return errors.New("invalid owned ingress rule")
		}
		ones, bits := network.Mask.Size()
		if bits != 32 || ones != 32 || p.IpProtocol != "tcp" || p.PortRange != "22/22" || p.SecurityGroupRuleId == "" {
			return errors.New("owned rule has unexpected parameters")
		}
		var out struct{ RequestId string }
		if err = c.API.Call(ctx, "ecs", "RevokeSecurityGroup", map[string]string{"RegionId": c.Region, "SecurityGroupId": sg, "SecurityGroupRuleId.1": p.SecurityGroupRuleId}, &out); err != nil {
			return err
		}
	}
	rules, err = permissions(ctx, c, sg)
	if err != nil {
		return err
	}
	for _, p := range rules {
		if p.Description == ruleDescription(l) {
			return errors.New("owned ingress rule still exists")
		}
	}
	return nil
}
