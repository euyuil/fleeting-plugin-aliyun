// Package experimental contains an opt-in standby/stop controller for acceptance
// work. It is not linked into the production provider and declares no capability.
package experimental

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
	upstream "gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

// Controller must be the sole writer of its explicitly dedicated experiment pool.
// Call Reconcile to advance asynchronous transitions; no background loop is owned.
var ErrPoolNotReady = errors.New("experiment pool snapshot is not yet consistent")

type Controller struct {
	Client *aliyun.Client
	mu     sync.Mutex
}

func (c *Controller) Request(ctx context.Context, mode string, ids []string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if mode != "suspend" && mode != "resume" {
		return nil, errors.New("experiment mode must be suspend or resume")
	}
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 0 || len(ids) > 20 {
		return nil, errors.New("experiment requires 1 through 20 explicit IDs")
	}
	g, err := c.Client.Group(ctx)
	if err != nil {
		return nil, err
	}
	j, err := aliyun.ReadJournal(g)
	if err != nil {
		return nil, err
	}
	if j != nil {
		if j.Kind != mode || !slices.Equal(j.IDs, ids) {
			return nil, errors.New("another operation is pending")
		}
	} else {
		b := make([]byte, 24)
		if _, err = rand.Read(b); err != nil {
			return nil, err
		}
		if g.Tag("fleeting-mode") != "suspend" || g.Tag("fleeting-project") == "" || g.Tag("fleeting-run") == "" {
			return nil, errors.New("experiment requires an exclusively claimed suspend fixture")
		}
		members, e := c.Client.Members(ctx)
		if e != nil {
			return nil, e
		}
		allIDs := make([]string, 0, len(members))
		for id := range members {
			allIDs = append(allIDs, id)
		}
		instances, e := c.Client.Instances(ctx, allIDs)
		if e != nil {
			return nil, e
		}
		if e = c.validatePool(ctx, g, ids, members, instances); e != nil {
			return nil, e
		}

		j = &aliyun.Journal{Kind: mode, Token: hex.EncodeToString(b), Before: g.DesiredCapacity, Target: g.DesiredCapacity, IDs: ids, CreatedAt: time.Now().UTC()}
		if err = c.Client.StoreJournal(ctx, *j); err != nil {
			return nil, err
		}
	}
	return c.reconcile(ctx, j)
}
func (c *Controller) Reconcile(ctx context.Context) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	g, err := c.Client.Group(ctx)
	if err != nil {
		return nil, err
	}
	j, err := aliyun.ReadJournal(g)
	if err != nil {
		return nil, err
	}
	if j == nil {
		return nil, nil
	}
	if j.Kind != "suspend" && j.Kind != "resume" {
		return nil, errors.New("another operation owns the journal")
	}
	return c.reconcile(ctx, j)
}
func stageToken(base, stage, id string) string {
	sum := sha256.Sum256([]byte(base + "/" + stage + "/" + id))
	return hex.EncodeToString(sum[:])
}
func (c *Controller) reconcile(ctx context.Context, j *aliyun.Journal) ([]string, error) {
	g, err := c.Client.Group(ctx)
	if err != nil {
		return nil, err
	}
	if g.Tag("fleeting-mode") != "suspend" || g.Tag("fleeting-project") == "" || g.Tag("fleeting-run") == "" {
		return nil, errors.New("experiment requires an exclusively claimed suspend fixture")
	}
	if g.DesiredCapacity != j.Before || g.LifecycleState != "Active" || g.SpotInstanceRemedy || g.AutoRebalance {
		return nil, errors.New("experiment group intent or replacement policy changed")
	}
	members, err := c.Client.Members(ctx)
	if err != nil {
		return nil, err
	}
	allIDs := make([]string, 0, len(members))
	for id := range members {
		allIDs = append(allIDs, id)
	}
	instances, err := c.Client.Instances(ctx, allIDs)
	if err != nil {
		return nil, err
	}
	if err = c.validatePool(ctx, g, j.IDs, members, instances); err != nil {
		return nil, err
	}
	var accepted []string
	allDone := true
	for _, id := range j.IDs {
		m, member := members[id]
		i, exists := instances[id]
		if !member || !exists {
			return accepted, upstream.ErrInstanceUnhealthy
		}
		check := m
		if check.LifecycleState == "Standby" {
			check.LifecycleState = "InService"
		}
		if err = aliyun.ValidateTestMember(check); err != nil {
			return accepted, err
		}
		var action string
		q := map[string]string{"RegionId": c.Client.Region}
		if j.Kind == "suspend" {
			switch {
			case m.LifecycleState == "InService" && i.Status == "Running":
				action = "EnterStandby"
				q["ScalingGroupId"] = c.Client.GroupID
				q["InstanceId.1"] = id
				q["Async"] = "false"
				q["ClientToken"] = stageToken(j.Token, action, id)
			case m.LifecycleState == "Standby" && i.Status == "Running":
				action = "StopInstance"
				q["InstanceId"] = id
				q["StoppedMode"] = "StopCharging"
				q["ForceStop"] = "false"
			case m.LifecycleState == "Standby" && i.Status == "Stopped":
				accepted = append(accepted, id)
				continue
			case m.LifecycleState == "Standby" && i.Status == "Stopping":
				accepted = append(accepted, id)
				allDone = false
				continue
			default:
				return accepted, errors.New("suspend transition is not ready")
			}
		} else {
			switch {
			case m.LifecycleState == "Standby" && i.Status == "Stopped":
				action = "StartInstance"
				q["InstanceId"] = id
				q["InitLocalDisk"] = "false"
			case m.LifecycleState == "Standby" && i.Status == "Running":
				action = "ExitStandby"
				q["ScalingGroupId"] = c.Client.GroupID
				q["InstanceId.1"] = id
				q["Async"] = "false"
				q["ClientToken"] = stageToken(j.Token, action, id)
			case m.LifecycleState == "InService" && i.Status == "Running":
				accepted = append(accepted, id)
				continue
			case m.LifecycleState == "Standby" && i.Status == "Starting":
				accepted = append(accepted, id)
				allDone = false
				continue
			default:
				return accepted, errors.New("resume transition is not ready")
			}
		}
		service := "ecs"
		if action == "EnterStandby" || action == "ExitStandby" {
			service = "ess"
		}
		var out struct{ RequestId, ScalingActivityId string }
		if err = c.Client.API.Call(ctx, service, action, q, &out); err != nil {
			return accepted, err
		}
		accepted = append(accepted, id)
		allDone = false
	}
	if allDone {
		if err = c.Client.ClearJournal(ctx); err != nil {
			return accepted, err
		}
	}
	return accepted, nil
}

// validatePool admits only standby/stopped capacity belonging to the exact
// experimental target set. All remaining production policy guards still apply.
func (c *Controller) validatePool(ctx context.Context, g aliyun.Group, ids []string, members map[string]aliyun.Member, instances map[string]aliyun.Instance) error {
	if g.PendingCapacity != 0 || g.RemovingCapacity != 0 || g.TotalCapacity != g.DesiredCapacity || len(members) != g.TotalCapacity {
		return ErrPoolNotReady
	}
	for _, id := range ids {
		if _, ok := members[id]; !ok {
			return upstream.ErrInstanceUnhealthy
		}
	}
	standby := 0
	for id, member := range members {
		instance, ok := instances[id]
		if !ok {
			return upstream.ErrInstanceUnhealthy
		}
		target := slices.Contains(ids, id)
		if member.LifecycleState == "Standby" {
			if !target {
				return errors.New("another standby member is outside the experiment")
			}
			standby++
			member.LifecycleState = "InService"
		} else if member.LifecycleState != "InService" || instance.Status != "Running" {
			return errors.New("another member is not ready for the experiment")
		}
		if err := aliyun.ValidateTestMember(member); err != nil {
			return err
		}
	}
	if g.StandbyCapacity != standby || g.StoppedCapacity < 0 || g.StoppedCapacity > standby {
		return ErrPoolNotReady
	}
	checked := g
	checked.StandbyCapacity, checked.StoppedCapacity = 0, 0
	return c.Client.Validate(ctx, checked)
}

// State maps the standalone experimental path without advertising production support.
func State(member aliyun.Member, instance aliyun.Instance, intent string) upstream.State {
	if member.LifecycleState == "Standby" {
		switch instance.Status {
		case "Stopped":
			return upstream.StateSuspended
		case "Stopping":
			return upstream.StateSuspending
		case "Starting":
			return upstream.StateResuming
		case "Running":
			if intent == "resume" {
				return upstream.StateResuming
			}
			return upstream.StateSuspending
		}
	}
	if member.LifecycleState == "InService" && instance.Status == "Running" {
		return upstream.StateRunning
	}
	return upstream.StateCreating
}
