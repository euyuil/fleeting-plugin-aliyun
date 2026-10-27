package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

func jsonUnmarshal(b []byte, out any) error { return json.Unmarshal(b, out) }
func Observe(ctx context.Context, c *aliyun.Client, l *Ledger, typ, image, zone string) error {
	return ObserveBudget(ctx, c, l, 1, typ, image, zone)
}
func ObserveBudget(ctx context.Context, c *aliyun.Client, l *Ledger, budget int, typ, image, zone string) error {
	if budget < 1 || budget > 3 {
		return errors.New("extended creation budget must be between one and three")
	}
	g, err := c.Group(ctx)
	if err != nil {
		return err
	}
	if g.Tag(RunTag) != l.Marker() || g.Tag(ProjectTag) != l.Project {
		return errors.New("run ownership changed")
	}
	members, err := c.Members(ctx)
	if err != nil {
		return err
	}
	for id, m := range members {
		if err = aliyun.ValidateTestMember(m); err != nil {
			return err
		}
		if !slices.ContainsFunc(l.Instances, func(r Resource) bool { return r.InstanceID == id }) {
			l.Instances = append(l.Instances, Resource{InstanceID: id, Origin: "AutoCreated in exclusively claimed group"})
		}
	}
	if err = RecoverResources(ctx, c, l); err != nil {
		return err
	}

	if len(l.Instances) > budget {
		return fmt.Errorf("instance creation budget %d exceeded, including replacements", budget)
	}
	ids := make([]string, 0, len(l.Instances))
	for _, r := range l.Instances {
		ids = append(ids, r.InstanceID)
	}
	instances, err := c.Instances(ctx, ids)
	if err != nil {
		return err
	}
	for n, r := range l.Instances {
		if r.UnsafeDiskPolicy {
			return errors.New("actual worker disk policy does not release with instance")
		}
		i, exists := instances[r.InstanceID]
		if !exists {
			continue
		}
		if i.InstanceChargeType != "PostPaid" || (i.SpotStrategy != "SpotAsPriceGo" && i.SpotStrategy != "SpotWithPriceLimit") {
			return errors.New("actual ECS instance is not Spot")
		}
		if i.InstanceType != typ || i.ImageId != image || i.ZoneId != zone || i.EipAddress.AllocationId != "" {
			return errors.New("actual ECS resource violates fixed fixture allowlist")
		}
		l.Instances[n].SpotVerified = true
		l.Instances[n].Zone = i.ZoneId
		l.Instances[n].InstanceType = i.InstanceType
		if l.Instances[n].AutoReleaseAt == "" && i.Status == "Running" {
			at := l.StartedAt.Add(time.Hour).UTC().Format(time.RFC3339)
			var out struct{ RequestId string }
			if err = c.API.Call(ctx, "ecs", "ModifyInstanceAutoReleaseTime", map[string]string{"RegionId": c.Region, "InstanceId": i.InstanceId, "AutoReleaseTime": at}, &out); err != nil {
				return err
			}
			l.Instances[n].AutoReleaseAt = at
			if err = l.Save(); err != nil {
				return err
			}
		}

	}
	return nil
}
func appendUnique(ids []string, id string) []string {
	if !slices.Contains(ids, id) {
		return append(ids, id)
	}
	return ids
}
