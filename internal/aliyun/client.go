package aliyun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hashicorp/go-hclog"
	"strconv"
	"strings"
)

type Client struct {
	API             API
	Log             hclog.Logger
	Region, GroupID string
}

func (c *Client) call(ctx context.Context, service, action string, q map[string]string, out any) error {
	if q == nil {
		q = map[string]string{}
	}
	q["RegionId"] = c.Region
	err := c.API.Call(ctx, service, action, q, out)
	if c.Log != nil {
		if err == nil {
			b, _ := json.Marshal(out)
			var id struct{ RequestId string }
			_ = json.Unmarshal(b, &id)
			c.Log.Debug("cloud request completed", "action", action, "request_id", id.RequestId)
		} else {
			c.Log.Debug("cloud request failed", "action", action, "error", err)
		}
	}
	return err
}
func (c *Client) Group(ctx context.Context) (Group, error) {
	var out struct {
		ScalingGroups struct{ ScalingGroup []Group }
	}
	err := c.call(ctx, "ess", "DescribeScalingGroups", map[string]string{"ScalingGroupId.1": c.GroupID}, &out)
	if err != nil {
		return Group{}, err
	}
	if len(out.ScalingGroups.ScalingGroup) != 1 {
		return Group{}, errors.New("exact scaling group was not found")
	}
	g := out.ScalingGroups.ScalingGroup[0]
	if g.ScalingGroupId != c.GroupID || g.RegionId != c.Region {
		return Group{}, errors.New("group identity mismatch")
	}
	return g, nil
}
func (c *Client) Members(ctx context.Context) (map[string]Member, error) {
	all := map[string]Member{}
	expected := -1
	for page := 1; page <= 10000; page++ {
		var out struct {
			TotalCount       *int
			PageNumber       int
			ScalingInstances struct{ ScalingInstance []Member }
		}
		err := c.call(ctx, "ess", "DescribeScalingInstances", map[string]string{"ScalingGroupId": c.GroupID, "PageNumber": strconv.Itoa(page), "PageSize": "50"}, &out)
		if err != nil {
			return nil, err
		}
		if expected < 0 {
			if out.TotalCount == nil {
				return nil, errors.New("missing TotalCount in API response")
			}
			expected = *out.TotalCount
		}
		if out.TotalCount == nil || *out.TotalCount != expected {
			return nil, errors.New("membership changed during pagination; retry complete snapshot")
		}
		for _, m := range out.ScalingInstances.ScalingInstance {
			if m.InstanceId == "" || m.ScalingGroupId != c.GroupID {
				return nil, errors.New("member identity mismatch")
			}
			if _, exists := all[m.InstanceId]; exists {
				return nil, errors.New("duplicate member in paginated snapshot")
			}
			all[m.InstanceId] = m
		}
		if len(all) == expected {
			return all, nil
		}
		if len(out.ScalingInstances.ScalingInstance) == 0 || len(all) > expected {
			return nil, errors.New("incomplete membership pagination")
		}
	}
	return nil, errors.New("membership pagination limit exceeded")
}
func (c *Client) Instances(ctx context.Context, ids []string) (map[string]Instance, error) {
	all := map[string]Instance{}
	for start := 0; start < len(ids); start += 100 {
		batch := ids[start:min(start+100, len(ids))]
		b, _ := json.Marshal(batch)
		expected := -1
		count := 0
		for page := 1; page <= 100; page++ {
			var out struct {
				TotalCount *int
				Instances  struct{ Instance []Instance }
			}
			err := c.call(ctx, "ecs", "DescribeInstances", map[string]string{"InstanceIds": string(b), "PageSize": "100", "PageNumber": strconv.Itoa(page)}, &out)
			if err != nil {
				return nil, err
			}
			if expected < 0 {
				if out.TotalCount == nil {
					return nil, errors.New("missing TotalCount in API response")
				}
				expected = *out.TotalCount
			}
			if out.TotalCount == nil || *out.TotalCount != expected {
				return nil, errors.New("ECS count changed during pagination")
			}
			for _, i := range out.Instances.Instance {
				found := false
				for _, id := range batch {
					if id == i.InstanceId {
						found = true
						break
					}
				}
				if !found {
					return nil, errors.New("ECS returned an unrequested instance")
				}
				if _, ok := all[i.InstanceId]; ok {
					return nil, errors.New("duplicate ECS instance")
				}
				all[i.InstanceId] = i
				count++
			}
			if count == expected {
				break
			}
			if len(out.Instances.Instance) == 0 || count > expected {
				return nil, errors.New("incomplete ECS pagination")
			}
			if page == 100 {
				return nil, errors.New("ECS pagination limit exceeded")
			}
		}
	}
	return all, nil
}
func (c *Client) SetCapacity(ctx context.Context, target int, token string) error {
	var out struct{ RequestId, ScalingActivityId string }
	return c.call(ctx, "ess", "ScaleWithAdjustment", map[string]string{"ScalingGroupId": c.GroupID, "AdjustmentType": "TotalCapacity", "AdjustmentValue": strconv.Itoa(target), "SyncActivity": "false", "ClientToken": token}, &out)
}
func (c *Client) Remove(ctx context.Context, ids []string, token string) (Removal, error) {
	return c.RemoveWithDesired(ctx, ids, token, true)
}
func (c *Client) RemoveWithDesired(ctx context.Context, ids []string, token string, decrease bool) (Removal, error) {
	q := map[string]string{"ScalingGroupId": c.GroupID, "RemovePolicy": "release", "DecreaseDesiredCapacity": strconv.FormatBool(decrease), "IgnoreInvalidInstance": "true", "ClientToken": token, "StopInstanceTimeout": "60"}
	for n, id := range ids {
		q[fmt.Sprintf("InstanceId.%d", n+1)] = id
	}
	var out Removal
	err := c.call(ctx, "ess", "RemoveInstances", q, &out)
	return out, err
}
func (c *Client) Activity(ctx context.Context, id string) (Activity, error) {
	var out struct {
		ScalingActivities struct{ ScalingActivity []Activity }
	}
	err := c.call(ctx, "ess", "DescribeScalingActivities", map[string]string{"ScalingActivityId.1": id}, &out)
	if err != nil {
		return Activity{}, err
	}
	if len(out.ScalingActivities.ScalingActivity) != 1 {
		return Activity{}, errors.New("activity not yet observable")
	}
	a := out.ScalingActivities.ScalingActivity[0]
	if a.ScalingGroupId != c.GroupID || a.ScalingActivityId != id {
		return Activity{}, errors.New("activity identity mismatch")
	}
	return a, nil
}
func (c *Client) Validate(ctx context.Context, g Group) error {
	if g.LifecycleState != "Active" || g.GroupType != "ECS" || !g.EnableDesiredCapacity || g.MinSize != 0 || g.MaxSize <= 0 || g.MaxSize > 2000 || g.DesiredCapacity < 0 || g.DesiredCapacity > g.MaxSize {
		return errors.New("group must be Active ECS with desired capacity, MinSize=0 and finite MaxSize")
	}
	if g.LaunchTemplateId == "" {
		return errors.New("a pinned launch template is required")
	}
	v, err := strconv.Atoi(g.LaunchTemplateVersion)
	if err != nil || v < 1 {
		return errors.New("launch template version must be a positive number")
	}
	if g.MaxInstanceLifetime != 0 || g.ScalingPolicy != "release" || g.SpotInstanceRemedy || g.AutoRebalance || g.AzBalance || g.SystemSuspended || len(g.SuspendedProcesses.SuspendedProcess) > 0 || g.ProtectedCapacity+g.StandbyCapacity+g.StoppedCapacity+g.PendingWaitCapacity+g.RemovingWaitCapacity != 0 {
		return errors.New("group has unsupported replacement, recycle, standby, protection or lifecycle policy")
	}
	for _, o := range g.LaunchTemplateOverrides.LaunchTemplateOverride {
		if o.WeightedCapacity != 0 && o.WeightedCapacity != 1 {
			return errors.New("instance weights must equal one")
		}
	}
	for _, a := range []string{"DescribeScalingRules", "DescribeScheduledTasks", "DescribeLifecycleHooks"} {
		var out struct{ TotalCount *int }
		q := map[string]string{"ScalingGroupId": c.GroupID, "PageSize": "50"}
		if a == "DescribeScheduledTasks" {
			q["TaskEnabled"] = "true"
		}
		if err := c.call(ctx, "ess", a, q, &out); err != nil {
			return err
		}
		if out.TotalCount == nil || *out.TotalCount < 0 {
			return fmt.Errorf("missing or invalid policy count: %s", a)
		}
		if *out.TotalCount != 0 {
			return fmt.Errorf("conflicting group configuration: %s", a)
		}
	}
	next := ""
	seen := map[string]bool{}
	tasks := map[string]bool{}
	expected := -1
	for {
		var out struct {
			NextToken            string
			TotalCount           *int
			InstanceRefreshTasks struct {
				InstanceRefreshTask []struct{ InstanceRefreshTaskId, ScalingGroupId, RegionId, Status string }
			}
		}
		if err := c.call(ctx, "ess", "DescribeInstanceRefreshes", map[string]string{"ScalingGroupId": c.GroupID, "MaxResults": "50", "NextToken": next}, &out); err != nil {
			return err
		}
		if out.TotalCount == nil || *out.TotalCount < 0 {
			return errors.New("missing or invalid refresh task count")
		}
		if expected < 0 {
			expected = *out.TotalCount
		}
		if *out.TotalCount != expected {
			return errors.New("refresh tasks changed during pagination")
		}
		for _, task := range out.InstanceRefreshTasks.InstanceRefreshTask {
			if task.ScalingGroupId != c.GroupID || task.RegionId != c.Region || task.InstanceRefreshTaskId == "" {
				return errors.New("refresh identity mismatch")
			}
			if tasks[task.InstanceRefreshTaskId] {
				return errors.New("duplicate refresh task")
			}
			tasks[task.InstanceRefreshTaskId] = true
			switch task.Status {
			case "Successful", "Cancelled", "Failed", "RollbackSuccessful", "RollbackFailed":
			default:
				return errors.New("active instance refresh is unsupported")
			}
		}
		if len(tasks) > expected || (out.NextToken == "" && len(tasks) != expected) || (out.NextToken != "" && len(out.InstanceRefreshTasks.InstanceRefreshTask) == 0) {
			return errors.New("incomplete refresh task pagination")
		}
		if out.NextToken == "" {
			break
		}
		if seen[out.NextToken] {
			return errors.New("repeated refresh pagination token")
		}
		seen[out.NextToken] = true
		next = out.NextToken
	}

	return nil
}
func ValidateMember(m Member) error {
	if m.ReplaceStatus != "" && m.ReplaceStatus != "Normal" {
		return errors.New("member is undergoing automatic replacement")
	}
	if m.CreationType != "AutoCreated" && !((m.CreationType == "Attached" || m.CreationType == "Managed") && m.Entrusted && strings.HasPrefix(m.InstanceId, "i-")) {
		return fmt.Errorf("member %s is not verified as releasable by ESS", m.InstanceId)
	}
	if m.WeightedCapacity != 0 && m.WeightedCapacity != 1 {
		return errors.New("member weight must equal one")
	}
	switch m.LifecycleState {
	case "Protected", "Standby", "Pending:Wait", "Removing:Wait":
		return errors.New("member has unsupported lifecycle state")
	}
	return nil
}
func (c *Client) Architecture(ctx context.Context, typ string) (string, error) {
	var out struct {
		InstanceTypes struct {
			InstanceType []struct{ InstanceTypeId, CpuArchitecture string }
		}
	}
	err := c.call(ctx, "ecs", "DescribeInstanceTypes", map[string]string{"InstanceTypes.1": typ}, &out)
	if err != nil {
		return "", err
	}
	for _, t := range out.InstanceTypes.InstanceType {
		if t.InstanceTypeId == typ {
			switch t.CpuArchitecture {
			case "x86_64", "x64":
				return "amd64", nil
			case "arm64", "aarch64":
				return "arm64", nil
			}
		}
	}
	return "", errors.New("unsupported or unknown CPU architecture; set connector arch explicitly")
}
func (c *Client) Template(ctx context.Context, g Group) (Template, error) {
	var out struct {
		LaunchTemplateVersionSets struct{ LaunchTemplateVersionSet []Template }
	}
	err := c.call(ctx, "ecs", "DescribeLaunchTemplateVersions", map[string]string{"LaunchTemplateId": g.LaunchTemplateId, "LaunchTemplateVersion.1": g.LaunchTemplateVersion, "DetailFlag": "true"}, &out)
	if err != nil {
		return Template{}, err
	}
	if len(out.LaunchTemplateVersionSets.LaunchTemplateVersionSet) != 1 {
		return Template{}, errors.New("pinned template was not found")
	}
	t := out.LaunchTemplateVersionSets.LaunchTemplateVersionSet[0]
	if t.LaunchTemplateId != g.LaunchTemplateId || strconv.Itoa(t.VersionNumber) != g.LaunchTemplateVersion {
		return Template{}, errors.New("template version mismatch")
	}
	return t, nil
}

func ValidateTestMember(m Member) error {
	if m.CreationType != "AutoCreated" {
		return errors.New("live fixture member was not automatically created by ESS")
	}
	return ValidateMember(m)
}
