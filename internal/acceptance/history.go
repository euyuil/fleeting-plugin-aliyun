package acceptance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

var ErrInventoryNotReady = errors.New("associated resources are not yet visible")

type HistoricalActivity struct {
	ScalingActivityId, ScalingGroupId, StatusCode, StartTime string
	CreatedCapacity                                          int
	CreatedInstances                                         struct{ CreatedInstance []string }
}

func History(ctx context.Context, c *aliyun.Client) ([]HistoricalActivity, error) {
	var all []HistoricalActivity
	seen := map[string]bool{}
	expected := -1
	for page := 1; page <= 100; page++ {
		var out struct {
			TotalCount        *int
			ScalingActivities struct{ ScalingActivity []HistoricalActivity }
		}
		if err := c.API.Call(ctx, "ess", "DescribeScalingActivities", map[string]string{"RegionId": c.Region, "ScalingGroupId": c.GroupID, "PageNumber": fmt.Sprint(page), "PageSize": "50"}, &out); err != nil {
			return nil, err
		}
		if out.TotalCount == nil || *out.TotalCount < 0 {
			return nil, errors.New("missing activity count")
		}
		if expected < 0 {
			expected = *out.TotalCount
		}
		if expected != *out.TotalCount {
			return nil, errors.New("activity history changed during pagination; retry complete history")
		}
		for _, a := range out.ScalingActivities.ScalingActivity {
			if a.ScalingActivityId == "" || seen[a.ScalingActivityId] || a.ScalingGroupId != c.GroupID || a.CreatedCapacity < 0 {
				return nil, errors.New("invalid or repeated activity ID")
			}
			switch a.StatusCode {
			case "Successful", "Warning", "Failed", "Rejected", "InProgress":
			default:
				return nil, errors.New("unknown activity state")
			}
			ids := map[string]bool{}
			for _, id := range a.CreatedInstances.CreatedInstance {
				if !strings.HasPrefix(id, "i-") || len(id) <= 2 || ids[id] {
					return nil, errors.New("invalid activity instance identity")
				}
				ids[id] = true
			}
			seen[a.ScalingActivityId] = true
			all = append(all, a)
		}
		if len(all) == expected {
			return all, nil
		}
		if len(all) > expected || len(out.ScalingActivities.ScalingActivity) == 0 {
			return nil, errors.New("incomplete activity history")
		}
	}
	return nil, errors.New("activity history pagination limit exceeded")
}
func RecoverResources(ctx context.Context, c *aliyun.Client, l *Ledger) error {
	group, groupErr := c.Group(ctx)
	if groupErr != nil {
		return groupErr
	}
	if l.HistoryBaselineKnown && group.Tag("fleeting-baseline") != baselineDigest(l.BaselineActivities) {
		l.HistoryBaselineKnown = false
		l.EvidenceIncomplete = true
	}
	history, err := History(ctx, c)
	if err != nil {
		return err
	}
	if !l.HistoryBaselineKnown {
		l.EvidenceIncomplete = true
	}
	pendingIDs := map[string]bool{}
	for _, a := range history {
		if l.HistoryBaselineKnown && slices.Contains(l.BaselineActivities, a.ScalingActivityId) {
			continue
		}
		if !l.HistoryBaselineKnown {
			at, e := time.Parse(time.RFC3339, a.StartTime)
			if e != nil {
				at, e = time.Parse("2006-01-02T15:04Z", a.StartTime)
			}
			if e != nil {
				return errors.New("cannot attribute historical activity")
			}
			if at.Before(l.StartedAt.Add(-time.Minute)) {
				continue
			}
		}
		if a.StatusCode != "InProgress" && a.CreatedCapacity != len(a.CreatedInstances.CreatedInstance) {
			l.EvidenceIncomplete = true
		}
		for _, id := range a.CreatedInstances.CreatedInstance {
			if a.StatusCode == "InProgress" {
				pendingIDs[id] = true
			}
			if !slices.ContainsFunc(l.Instances, func(r Resource) bool { return r.InstanceID == id }) {
				l.Instances = append(l.Instances, Resource{InstanceID: id, Origin: "new ESS activity after claimed baseline"})
			}
		}
	}
	ids := []string{}
	for _, r := range l.Instances {
		ids = append(ids, r.InstanceID)
	}
	members, err := c.Members(ctx)
	if err != nil {
		return err
	}
	instances, err := c.Instances(ctx, ids)
	if err != nil {
		return err
	}
	for n, r := range l.Instances {

		instance, ok := instances[r.InstanceID]
		if !ok {
			_, member := members[r.InstanceID]
			if !r.InventoryComplete && !member && !pendingIDs[r.InstanceID] {
				l.EvidenceIncomplete = true
			}
			continue
		}
		if instance.Status != "Running" && instance.Status != "Stopped" {
			continue
		}
		if err = inventory(ctx, c, &l.Instances[n]); err != nil {
			if errors.Is(err, ErrInventoryNotReady) {
				l.Instances[n].InventoryComplete = false
				continue
			}
			return err
		}
	}
	return nil
}
func inventory(ctx context.Context, c *aliyun.Client, r *Resource) error {
	var disks struct {
		TotalCount *int
		Disks      struct {
			Disk []struct {
				DiskId, InstanceId, Type string
				DeleteWithInstance       bool
			}
		}
	}
	if err := c.API.Call(ctx, "ecs", "DescribeDisks", map[string]string{"RegionId": c.Region, "InstanceId": r.InstanceID, "PageSize": "100"}, &disks); err != nil {
		return err
	}
	if disks.TotalCount == nil || *disks.TotalCount != len(disks.Disks.Disk) {
		return errors.New("incomplete disk inventory")
	}
	if len(disks.Disks.Disk) == 0 {
		return ErrInventoryNotReady
	}
	seenDisks := map[string]bool{}
	systemDisks := 0
	for _, d := range disks.Disks.Disk {
		if d.DiskId == "" || seenDisks[d.DiskId] || d.InstanceId != r.InstanceID || (d.Type != "system" && d.Type != "data") {
			return errors.New("invalid disk identity or attachment")
		}
		seenDisks[d.DiskId] = true
		if d.Type == "system" {
			systemDisks++
		}
		r.DiskIDs = appendUnique(r.DiskIDs, d.DiskId)
		if !d.DeleteWithInstance {
			r.UnsafeDiskPolicy = true
		}
	}
	if systemDisks != 1 {
		return errors.New("exactly one system disk must be inventoried")
	}
	var enis struct {
		TotalCount           *int
		NetworkInterfaceSets struct {
			NetworkInterfaceSet []struct{ NetworkInterfaceId, InstanceId, Type string }
		}
	}
	if err := c.API.Call(ctx, "ecs", "DescribeNetworkInterfaces", map[string]string{"RegionId": c.Region, "InstanceId": r.InstanceID, "PageSize": "100"}, &enis); err != nil {
		return err
	}
	if enis.TotalCount == nil || *enis.TotalCount != len(enis.NetworkInterfaceSets.NetworkInterfaceSet) {
		return errors.New("incomplete ENI inventory")
	}
	if len(enis.NetworkInterfaceSets.NetworkInterfaceSet) == 0 {
		return ErrInventoryNotReady
	}
	seenENIs := map[string]bool{}
	primaryENIs := 0
	for _, e := range enis.NetworkInterfaceSets.NetworkInterfaceSet {
		if e.NetworkInterfaceId == "" || seenENIs[e.NetworkInterfaceId] || e.InstanceId != r.InstanceID || (e.Type != "Primary" && e.Type != "Secondary") {
			return errors.New("invalid ENI identity or attachment")
		}
		seenENIs[e.NetworkInterfaceId] = true
		if e.Type == "Primary" {
			primaryENIs++
		}
		r.NetworkInterfaceIDs = appendUnique(r.NetworkInterfaceIDs, e.NetworkInterfaceId)
	}
	if primaryENIs != 1 {
		return errors.New("exactly one primary ENI must be inventoried")
	}
	r.InventoryComplete = true
	return nil
}

func historyQuiet(ctx context.Context, c *aliyun.Client) (bool, error) {
	activities, err := History(ctx, c)
	if err != nil {
		return false, err
	}
	for _, a := range activities {
		if a.StatusCode == "InProgress" {
			return false, nil
		}
	}
	return true, nil
}
