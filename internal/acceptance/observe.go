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
		return productViolation(fmt.Sprintf("instance creation budget %d exceeded, including replacements", budget))
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
			return productViolation("actual worker disk policy does not release with instance")
		}
		i, exists := instances[r.InstanceID]
		if !exists {
			continue
		}
		if i.InstanceChargeType != "PostPaid" || (i.SpotStrategy != "SpotAsPriceGo" && i.SpotStrategy != "SpotWithPriceLimit") {
			return productViolation("actual ECS instance is not Spot")
		}
		if i.InstanceType != typ || i.ImageId != image || i.ZoneId != zone || i.EipAddress.AllocationId != "" {
			return productViolation("actual ECS resource violates fixed fixture allowlist")
		}
		l.Instances[n].SpotVerified = true
		l.Instances[n].Zone = i.ZoneId
		l.Instances[n].InstanceType = i.InstanceType
		if l.Instances[n].AutoReleaseAt == "" && i.Status == "Running" {
			ceiling := l.WorkerExpiresAt
			if ceiling.IsZero() {
				// Legacy plugin-only fixtures retain their original one-hour TTL;
				// ExpiresAt is their separate, shorter janitor eligibility marker.
				ceiling = l.StartedAt.Add(time.Hour).UTC().Truncate(time.Minute)
			}
			at, write, err := workerAutoReleaseDeadline(l.StartedAt, ceiling, time.Now(), i.AutoReleaseTime)
			if err != nil {
				return err
			}
			if write {
				var out struct{ RequestId string }
				if err = c.API.Call(ctx, "ecs", "ModifyInstanceAutoReleaseTime", map[string]string{"RegionId": c.Region, "InstanceId": i.InstanceId, "AutoReleaseTime": at}, &out); err != nil {
					return err
				}
				if err = confirmWorkerAutoRelease(ctx, c, i.InstanceId, at); err != nil {
					return err
				}
			}
			l.Instances[n].AutoReleaseAt = at
			if err = l.Save(); err != nil {
				return err
			}
		}

	}
	return nil
}

func workerAutoReleaseDeadline(started, ceiling, now time.Time, existing string) (string, bool, error) {
	if started.IsZero() || started.After(now) || !ceiling.After(started) || ceiling.After(started.Add(90*time.Minute)) {
		return "", false, errors.New("invalid run start for worker automatic release")
	}
	if existing != "" {
		// DescribeInstances can return minute-precision UTC. Only its actual
		// instance response proves that a launch-template deadline was inherited.
		at, err := time.Parse(time.RFC3339, existing)
		if err != nil {
			at, err = time.Parse("2006-01-02T15:04Z", existing)
		}
		if err != nil {
			return "", false, errors.New("worker automatic release deadline has an invalid format")
		}
		if !at.After(now) {
			return "", false, errors.New("worker automatic release deadline has expired")
		}
		if at.After(ceiling) {
			return "", false, errors.New("worker automatic release deadline exceeds the fixed scenario limit")
		}
		return at.UTC().Format(time.RFC3339), false, nil
	}
	// ECS rounds down to the minute and requires 30 minutes of lead time for
	// a new setting. Leave another minute for transmission; never roll the cap
	// forward for a late worker or rewrite its already-established deadline.
	if ceiling.Before(now.Add(31 * time.Minute)) {
		return "", false, errors.New("insufficient lead time to establish the fixed worker automatic release deadline")
	}
	return ceiling.Format(time.RFC3339), true, nil
}

func confirmWorkerAutoRelease(ctx context.Context, c *aliyun.Client, id, expected string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	want, _ := time.Parse(time.RFC3339, expected)
	for {
		instances, err := c.Instances(ctx, []string{id})
		if err != nil {
			return err
		}
		instance, exists := instances[id]
		if !exists {
			return errors.New("worker disappeared before automatic release confirmation")
		}
		if instance.AutoReleaseTime != "" {
			actual, err := time.Parse(time.RFC3339, instance.AutoReleaseTime)
			if err != nil {
				actual, err = time.Parse("2006-01-02T15:04Z", instance.AutoReleaseTime)
			}
			if err != nil || !actual.Equal(want) {
				return errors.New("worker automatic release readback differs from the fixed deadline")
			}
			return nil
		}
		if err := aliyun.Wait(ctx, time.Second); err != nil {
			return errors.New("worker automatic release was not confirmed before its readback deadline")
		}
	}
}

func appendUnique(ids []string, id string) []string {
	if !slices.Contains(ids, id) {
		return append(ids, id)
	}
	return ids
}
