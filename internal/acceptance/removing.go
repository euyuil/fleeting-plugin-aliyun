package acceptance

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

type RemovingSnapshot struct {
	ObservedAt      time.Time `json:"observed_at"`
	ActivityID      string    `json:"activity_id"`
	ActivityStatus  string    `json:"activity_status"`
	ActivityTrigger string    `json:"activity_trigger_type"`
	ActivityIDs     []string  `json:"scaling_activity_ids"`
	JournalKind     string    `json:"journal_kind"`
	JournalTarget   int       `json:"journal_target"`
	InstanceID      string    `json:"instance_id"`
	Desired         int       `json:"desired_capacity"`
	Total           int       `json:"total_capacity"`
	Removing        int       `json:"removing_capacity"`
	MemberLifecycle string    `json:"member_lifecycle"`
	ECSState        string    `json:"ecs_state"`
}

// CaptureRemoving only observes an accepted one-worker removal. The target must
// still exist in both ESS and ECS; a completed deletion cannot satisfy this mode.
func CaptureRemoving(ctx context.Context, c *aliyun.Client, l *Ledger, id string, beforeActivities []string) (RemovingSnapshot, error) {
	var s RemovingSnapshot
	if len(l.Instances) != 1 || l.Instances[0].InstanceID != id || !l.Instances[0].SpotVerified || !l.Instances[0].InventoryComplete || l.EvidenceIncomplete {
		return s, errors.New("removal cutpoint requires one fully inventoried Spot worker")
	}
	g, err := c.Group(ctx)
	if err != nil {
		return s, err
	}
	if g.Tag(RunTag) != l.Marker() || g.Tag(ProjectTag) != l.Project || !l.HistoryBaselineKnown || g.Tag("fleeting-baseline") != baselineDigest(l.BaselineActivities) {
		return s, errors.New("removal cutpoint ownership or baseline changed")
	}
	if g.MinSize != 0 || g.MaxSize != 1 || g.LifecycleState != "Active" || g.ScalingPolicy != "release" || g.DesiredCapacity != 0 || g.TotalCapacity < 0 || g.TotalCapacity > 1 || g.PendingCapacity != 0 || g.RemovingCapacity < 0 || g.RemovingCapacity > 1 {
		return s, errors.New("removal cutpoint requires the owned one-to-zero intent")
	}
	j, err := aliyun.ReadJournal(g)
	if err != nil {
		return s, err
	}
	if j == nil || j.Kind != "remove" || j.Before != 1 || j.Target != 0 || j.Activity == "" || j.AcceptedMask != 1 || !slices.Equal(j.IDs, []string{id}) || slices.Contains(beforeActivities, j.Activity) {
		return s, errors.New("accepted target-specific removal journal is missing")
	}
	history, err := History(ctx, c)
	if err != nil {
		return s, err
	}
	for _, a := range history {
		s.ActivityIDs = append(s.ActivityIDs, a.ScalingActivityId)
		if a.ScalingActivityId == j.Activity {
			if a.StatusCode != "InProgress" || a.TriggerSourceType != "Api" || a.CreatedCapacity != 0 || len(a.CreatedInstances.CreatedInstance) != 0 {
				return s, errors.New("accepted removal activity is not in progress")
			}
			s.ActivityID, s.ActivityStatus, s.ActivityTrigger = a.ScalingActivityId, a.StatusCode, a.TriggerSourceType
		} else if !slices.Contains(beforeActivities, a.ScalingActivityId) {
			if a.TriggerSourceType != "Api" || a.StatusCode != "Successful" || a.CreatedCapacity != 0 || len(a.CreatedInstances.CreatedInstance) != 0 {
				return s, errors.New("unexpected activity accompanied the accepted removal")
			}
		}
	}
	if s.ActivityID == "" {
		return s, errors.New("accepted removal activity is not visible")
	}
	for _, id := range beforeActivities {
		if !slices.Contains(s.ActivityIDs, id) {
			return s, errors.New("pre-removal activity disappeared")
		}
	}
	slices.Sort(s.ActivityIDs)
	members, err := c.Members(ctx)
	if err != nil {
		return s, err
	}
	m, ok := members[id]
	if !ok || len(members) != 1 || m.ScalingGroupId != c.GroupID || m.LifecycleState != "Removing" {
		return s, errors.New("target is not the sole removing ESS member")
	}
	if err = aliyun.ValidateTestMember(m); err != nil {
		return s, err
	}
	instances, err := c.Instances(ctx, []string{id})
	if err != nil {
		return s, err
	}
	i, ok := instances[id]
	if !ok || len(instances) != 1 || (i.Status != "Running" && i.Status != "Stopping" && i.Status != "Stopped") {
		return s, errors.New("target ECS has already disappeared or has an unknown removal state")
	}
	s.ObservedAt = time.Now().UTC()
	s.JournalKind, s.JournalTarget, s.InstanceID = j.Kind, j.Target, id
	s.Desired, s.Total, s.Removing = g.DesiredCapacity, g.TotalCapacity, g.RemovingCapacity
	s.MemberLifecycle, s.ECSState = m.LifecycleState, i.Status
	return s, nil
}
