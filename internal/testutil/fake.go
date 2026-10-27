// Package testutil provides a stateful fake of the ESS/ECS API boundary.
package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

type Call struct {
	Service, Action string
	Query           map[string]string
}
type Fake struct {
	Mu                  sync.Mutex
	Group               aliyun.Group
	Members             map[string]aliyun.Member
	Instances           map[string]aliyun.Instance
	Calls               []Call
	Errors              map[string][]error
	After               func(string, map[string]string) error
	Before              func(string, map[string]string) error
	StaleGroups         []aliyun.Group
	Ignored             map[string]bool
	ActivityStatus      string
	Rules, Hooks, Tasks int
	Tokens              map[string]any
	removals            map[string][]string
	Limit               int
}

func New() *Fake {
	f := &Fake{Members: map[string]aliyun.Member{}, Instances: map[string]aliyun.Instance{}, Errors: map[string][]error{}, Tokens: map[string]any{}, Ignored: map[string]bool{}, removals: map[string][]string{}}
	f.Group = aliyun.Group{RegionId: "cn-test-1", ScalingGroupId: "asg-test", GroupType: "ECS", LifecycleState: "Active", EnableDesiredCapacity: true, MaxSize: 100, LaunchTemplateId: "lt-test", LaunchTemplateVersion: "1", ScalingPolicy: "release"}
	return f
}
func (f *Fake) Add(id string) {
	m := aliyun.Member{InstanceId: id, ScalingGroupId: f.Group.ScalingGroupId, CreationType: "AutoCreated", LifecycleState: "InService", HealthStatus: "Healthy", WeightedCapacity: 1}
	i := aliyun.Instance{InstanceId: id, Status: "Running", OSType: "linux", InstanceType: "ecs.test.small", SpotStrategy: "SpotAsPriceGo", InstanceChargeType: "PostPaid", ZoneId: "cn-test-1a", ImageId: "image-test"}
	i.PublicIpAddress.IpAddress = []string{"192.0.2.1"}
	i.VpcAttributes.PrivateIpAddress.IpAddress = []string{"10.0.0.1"}
	f.Members[id] = m
	f.Instances[id] = i
	f.Group.DesiredCapacity = len(f.Members)
	f.Group.TotalCapacity = len(f.Members)
}
func assign(dst, src any) error {
	b, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}
func (f *Fake) Call(ctx context.Context, service, action string, q map[string]string, out any) (callErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.Mu.Lock()
	defer f.Mu.Unlock()
	defer func() {
		if callErr == nil && f.After != nil {
			callErr = f.After(action, q)
		}
	}()
	copyQ := map[string]string{}
	for k, v := range q {
		copyQ[k] = v
	}
	f.Calls = append(f.Calls, Call{service, action, copyQ})
	if f.Before != nil {
		if err := f.Before(action, q); err != nil {
			return err
		}
	}
	if es := f.Errors[action]; len(es) > 0 {
		f.Errors[action] = es[1:]
		if es[0] != nil {
			return es[0]
		}
	}
	switch action {
	case "TagResources":
		for i := 1; i <= 20; i++ {
			key := q[fmt.Sprintf("Tag.%d.Key", i)]
			if key == "" {
				continue
			}
			value := q[fmt.Sprintf("Tag.%d.Value", i)]
			found := false
			for n, t := range f.Group.Tags.Tag {
				if t.TagKey == key {
					f.Group.Tags.Tag[n].TagValue = value
					found = true
				}
			}
			if !found {
				f.Group.Tags.Tag = append(f.Group.Tags.Tag, struct {
					TagKey, TagValue string
					Propagate        bool
				}{key, value, false})
			}
		}
		return assign(out, map[string]any{"RequestId": "test"})
	case "UntagResources":
		for i := 1; i <= 20; i++ {
			key := q[fmt.Sprintf("TagKey.%d", i)]
			f.Group.Tags.Tag = slices.DeleteFunc(f.Group.Tags.Tag, func(t struct {
				TagKey, TagValue string
				Propagate        bool
			}) bool {
				return t.TagKey == key
			})
		}
		return assign(out, map[string]any{"RequestId": "test"})
	case "DescribeScalingGroups":
		g := f.Group
		if len(f.StaleGroups) > 0 {
			g = f.StaleGroups[0]
			f.StaleGroups = f.StaleGroups[1:]
		}
		return assign(out, map[string]any{"ScalingGroups": map[string]any{"ScalingGroup": []aliyun.Group{g}}})
	case "DescribeScalingInstances":
		ids := sortedKeys(f.Members)
		page, _ := strconv.Atoi(q["PageNumber"])
		size, _ := strconv.Atoi(q["PageSize"])
		if f.Limit > 0 {
			size = f.Limit
		}
		start := min((page-1)*size, len(ids))
		end := min(start+size, len(ids))
		members := []aliyun.Member{}
		for _, id := range ids[start:end] {
			members = append(members, f.Members[id])
		}
		return assign(out, map[string]any{"TotalCount": len(ids), "PageNumber": page, "ScalingInstances": map[string]any{"ScalingInstance": members}})
	case "DescribeInstances":
		var ids []string
		_ = json.Unmarshal([]byte(q["InstanceIds"]), &ids)
		instances := []aliyun.Instance{}
		for _, id := range ids {
			if i, ok := f.Instances[id]; ok {
				instances = append(instances, i)
			}
		}
		return assign(out, map[string]any{"TotalCount": len(instances), "Instances": map[string]any{"Instance": instances}})
	case "ScaleWithAdjustment":
		t := q["ClientToken"]
		if _, ok := f.Tokens[t]; !ok {
			target, err := strconv.Atoi(q["AdjustmentValue"])
			if err != nil {
				return err
			}
			if q["AdjustmentType"] != "TotalCapacity" || q["SyncActivity"] != "false" {
				return fmt.Errorf("unsafe scaling request")
			}
			f.Group.DesiredCapacity = target
			f.Tokens[t] = target
		}
		return assign(out, map[string]any{"RequestId": "request-test"})
	case "RemoveInstances":
		if q["RemovePolicy"] != "release" || (q["DecreaseDesiredCapacity"] != "true" && q["DecreaseDesiredCapacity"] != "false") {
			return fmt.Errorf("unsafe removal")
		}
		t := q["ClientToken"]
		if prior, ok := f.Tokens[t]; ok {
			return assign(out, prior)
		}
		var result aliyun.Removal
		result.ScalingActivityId = fmt.Sprintf("activity-%d", len(f.removals))
		result.RequestId = "request-test"
		var accepted []string
		for n := 1; n <= 20; n++ {
			id := q[fmt.Sprintf("InstanceId.%d", n)]
			if id == "" {
				break
			}
			if f.Ignored[id] {
				result.IgnoredInstances.IgnoredInstance = append(result.IgnoredInstances.IgnoredInstance, struct{ InstanceId, Code string }{id, "InvalidInstance"})
				continue
			}
			if m, ok := f.Members[id]; ok {
				m.LifecycleState = "Removing"
				f.Members[id] = m
				accepted = append(accepted, id)
			}
		}
		if q["DecreaseDesiredCapacity"] == "true" {
			f.Group.DesiredCapacity -= len(accepted)
		}
		f.removals[result.ScalingActivityId] = accepted
		f.Tokens[t] = result
		return assign(out, result)
	case "DescribeLaunchTemplateVersions":
		return assign(out, map[string]any{"LaunchTemplateVersionSets": map[string]any{"LaunchTemplateVersionSet": []map[string]any{{"LaunchTemplateId": f.Group.LaunchTemplateId, "VersionNumber": 1, "LaunchTemplateData": map[string]any{}}}}})
	case "DescribeScalingActivities":
		if q["ScalingActivityId.1"] == "" {
			return assign(out, map[string]any{"TotalCount": 0, "ScalingActivities": map[string]any{"ScalingActivity": []any{}}})
		}
		status := f.ActivityStatus
		if status == "" {
			status = "Successful"
		}
		id := q["ScalingActivityId.1"]
		if status == "Successful" {
			for _, instance := range f.removals[id] {
				delete(f.Members, instance)
				delete(f.Instances, instance)
			}
			f.Group.TotalCapacity = len(f.Members)
		}
		return assign(out, map[string]any{"ScalingActivities": map[string]any{"ScalingActivity": []aliyun.Activity{{ScalingActivityId: id, ScalingGroupId: f.Group.ScalingGroupId, StatusCode: status}}}})
	case "DescribeInstanceRefreshes":
		return assign(out, map[string]any{"TotalCount": 0})
	case "DescribeScalingRules":
		return assign(out, map[string]any{"TotalCount": f.Rules})
	case "DescribeScheduledTasks":
		return assign(out, map[string]any{"TotalCount": f.Tasks})
	case "DescribeLifecycleHooks":
		return assign(out, map[string]any{"TotalCount": f.Hooks})
	case "DescribeInstanceTypes":
		return assign(out, map[string]any{"InstanceTypes": map[string]any{"InstanceType": []map[string]string{{"InstanceTypeId": "ecs.test.small", "CpuArchitecture": "x86_64"}}}})
	default:
		return fmt.Errorf("unhandled fake API: %s", action)
	}
}
func sortedKeys[V any](m map[string]V) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
