package acceptance

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

type CreatingSnapshot struct {
	ObservedAt      time.Time `json:"observed_at"`
	ActivityID      string    `json:"activity_id"`
	ActivityStatus  string    `json:"activity_status"`
	ActivityTrigger string    `json:"activity_trigger_type"`
	APIRecords      int       `json:"completed_zero_capacity_api_records"`
	ActivityIDs     []string  `json:"scaling_activity_ids"`
	JournalKind     string    `json:"journal_kind"`
	JournalTarget   int       `json:"journal_target"`
	InstanceID      string    `json:"instance_id,omitempty"`
	Desired         int       `json:"desired_capacity"`
	Total           int       `json:"total_capacity"`
	Pending         int       `json:"pending_capacity"`
	MemberCount     int       `json:"member_count"`
	MemberLifecycle string    `json:"member_lifecycle"`
	ECSState        string    `json:"ecs_state"`
}

// CaptureCreating is a read-only timing probe, not a membership snapshot for
// allocation or cleanup. An Init row may lack its instance ID; normal membership
// validation and resource attribution remain unchanged.
func CaptureCreating(ctx context.Context, c *aliyun.Client, l *Ledger) (CreatingSnapshot, error) {
	var s CreatingSnapshot
	g, err := c.Group(ctx)
	if err != nil {
		return s, err
	}
	if g.Tag(RunTag) != l.Marker() || g.Tag(ProjectTag) != l.Project || !l.HistoryBaselineKnown || g.Tag("fleeting-baseline") != baselineDigest(l.BaselineActivities) {
		return s, errors.New("creation cutpoint ownership or baseline changed")
	}
	if g.MinSize != 0 || g.MaxSize != 1 || g.LifecycleState != "Active" || g.ScalingPolicy != "release" || g.DesiredCapacity != 1 || g.TotalCapacity < 0 || g.TotalCapacity > 1 || g.PendingCapacity < 0 || g.PendingCapacity > 1 || g.RemovingCapacity != 0 {
		return s, errors.New("creation cutpoint requires the owned one-worker intent")
	}
	j, err := aliyun.ReadJournal(g)
	if err != nil {
		return s, err
	}
	if j == nil || j.Kind != "increase" || j.Before != 0 || j.Target != 1 || j.Count != 1 {
		return s, errors.New("accepted one-worker increase journal is missing")
	}
	s.JournalKind, s.JournalTarget = j.Kind, j.Target
	history, err := History(ctx, c)
	if err != nil {
		return s, err
	}
	for _, a := range history {
		s.ActivityIDs = append(s.ActivityIDs, a.ScalingActivityId)
		if !slices.Contains(l.BaselineActivities, a.ScalingActivityId) {
			// A successful desired-capacity API record can accompany the actual
			// ESS provisioning activity. It must contain no created capacity/IDs.
			if a.TriggerSourceType == "Api" && a.StatusCode == "Successful" && a.CreatedCapacity == 0 && len(a.CreatedInstances.CreatedInstance) == 0 {
				s.APIRecords++
				continue
			}
			if s.ActivityID != "" || a.StatusCode != "InProgress" || a.TriggerSourceType != "Ess" {
				return s, errors.New("creation cutpoint requires one ESS provisioning activity and only completed zero-capacity API records")
			}
			s.ActivityID, s.ActivityStatus = a.ScalingActivityId, a.StatusCode
			s.ActivityTrigger = a.TriggerSourceType
			if a.CreatedCapacity > 1 || len(a.CreatedInstances.CreatedInstance) > 1 {
				return s, errors.New("creation activity exceeds the one-worker budget")
			}
			if len(a.CreatedInstances.CreatedInstance) == 1 {
				s.InstanceID = a.CreatedInstances.CreatedInstance[0]
			}
		}
	}
	if s.ActivityID == "" {
		return s, errors.New("accepted creation activity is not visible")
	}
	slices.Sort(s.ActivityIDs)
	var out struct {
		TotalCount       *int
		ScalingInstances *struct{ ScalingInstance *[]aliyun.Member }
	}
	if err = c.API.Call(ctx, "ess", "DescribeScalingInstances", map[string]string{"RegionId": c.Region, "ScalingGroupId": c.GroupID, "PageNumber": "1", "PageSize": "100"}, &out); err != nil {
		return s, err
	}
	if out.TotalCount == nil || *out.TotalCount < 0 || *out.TotalCount > 1 || out.ScalingInstances == nil || out.ScalingInstances.ScalingInstance == nil || len(*out.ScalingInstances.ScalingInstance) != *out.TotalCount {
		return s, errors.New("creation cutpoint membership response is incomplete")
	}
	s.Desired, s.Total, s.Pending = g.DesiredCapacity, g.TotalCapacity, g.PendingCapacity
	s.MemberCount = *out.TotalCount
	s.MemberLifecycle, s.ECSState = "NotVisible", "NotAssigned"
	if s.MemberCount == 1 {
		m := (*out.ScalingInstances.ScalingInstance)[0]
		if m.ScalingGroupId != c.GroupID || (m.LifecycleState != "Init" && m.LifecycleState != "Pending") {
			return s, errors.New("worker is not in a verified creation lifecycle")
		}
		s.MemberLifecycle = m.LifecycleState
		if m.InstanceId != "" {
			if s.InstanceID != "" && s.InstanceID != m.InstanceId {
				return s, errors.New("activity and member instance identities disagree")
			}
			s.InstanceID = m.InstanceId
		}
	}
	if s.InstanceID != "" {
		instances, e := c.Instances(ctx, []string{s.InstanceID})
		if e != nil {
			return s, e
		}
		s.ECSState = "NotVisible"
		if i, ok := instances[s.InstanceID]; ok {
			if i.Status != "Pending" && i.Status != "Starting" && i.Status != "Stopped" {
				return s, errors.New("ECS is already running or has an unknown creation state")
			}
			s.ECSState = i.Status
		}
	}
	s.ObservedAt = time.Now().UTC()
	return s, nil
}
