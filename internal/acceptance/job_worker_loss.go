package acceptance

import (
	"context"
	"errors"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

type workerLossSequence struct {
	jobSequence
	loseWorker func(context.Context, JobEvidence) error
}

func (s workerLossSequence) run(ctx context.Context) error {
	r := s.result
	first, err := s.start(ctx, "worker-loss")
	if err != nil {
		return err
	}
	controlled := capacitySequence{jobSequence: s.jobSequence}
	for !first.Checkpoint {
		if err := s.checkManager(ctx); err != nil {
			return err
		}
		if err := s.observe(ctx); err != nil {
			return err
		}
		if err := controlled.poll(ctx, first); err != nil {
			return err
		}
		if err := aliyun.Wait(ctx, s.pollInterval); err != nil {
			return err
		}
	}
	if first.Status != "running" || len(r.Instances) != 1 {
		return errors.New("worker loss requires one real running job")
	}
	if err := s.loseWorker(ctx, *first); err != nil {
		return err
	}
	first.LossInjected = true
	if err := s.wait(ctx, first); err != nil {
		return err
	}
	// No scale-in or cancellation is permitted here. Runner must reconcile the
	// lost worker, and the group and associated resources must become empty.
	if err := s.release(ctx); err != nil {
		return err
	}
	if len(r.Instances) != 1 {
		return errors.New("worker loss created an unrequested replacement")
	}
	r.WorkerLossVerified = true
	if err := s.checkManager(ctx); err != nil {
		return err
	}
	second, err := s.start(ctx, "smoke")
	if err != nil {
		return err
	}
	if err := s.wait(ctx, second); err != nil {
		return err
	}
	if err := s.release(ctx); err != nil {
		return err
	}
	if err := s.checkManager(ctx); err != nil {
		return err
	}
	second.NormalRelease = true
	return verifyJobSequence(r)
}

func injectWorkerLoss(parent context.Context, cloud *aliyun.Client, ledger *Ledger, e JobEvidence) error {
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	if e.Scenario != "worker-loss" || e.Status != "running" || !e.Checkpoint || e.LossInjected || e.ControlReleased ||
		ledger.EvidenceIncomplete || len(ledger.Instances) != 1 || ledger.Region != cloud.Region || ledger.GroupID != cloud.GroupID {
		return errors.New("worker loss lacks an exclusive running fixture")
	}
	r := ledger.Instances[0]
	if r.InstanceID != e.InstanceID || !r.SpotVerified || !r.InventoryComplete || r.DeletionVerified || r.UnsafeDiskPolicy {
		return errors.New("worker loss lacks complete owned resource inventory")
	}
	g, err := cloud.Group(ctx)
	if err != nil {
		return err
	}
	if g.Tag(RunTag) != ledger.Marker() || g.Tag(ProjectTag) != ledger.Project || g.MaxSize != 1 || g.MinSize != 0 {
		return errors.New("worker loss fixture ownership or capacity changed")
	}
	members, err := cloud.Members(ctx)
	if err != nil {
		return err
	}
	m, exists := members[e.InstanceID]
	if !exists || len(members) != 1 || m.LifecycleState != "InService" || aliyun.ValidateTestMember(m) != nil {
		return errors.New("worker loss target is not the exclusively owned active member")
	}
	instances, err := cloud.Instances(ctx, []string{e.InstanceID})
	if err != nil {
		return err
	}
	worker, exists := instances[e.InstanceID]
	if !exists || worker.Status != "Running" || worker.InstanceChargeType != "PostPaid" ||
		(worker.SpotStrategy != "SpotAsPriceGo" && worker.SpotStrategy != "SpotWithPriceLimit") {
		return errors.New("worker loss target is not a running Spot instance")
	}
	// ForceStop models abrupt loss, outside the plugin's RemoveInstances path.
	// Any failure is fatal to acceptance; never select another deletion target.
	var out struct{ RequestId string }
	return cloud.API.Call(ctx, "ecs", "DeleteInstance", map[string]string{
		"RegionId": cloud.Region, "InstanceId": e.InstanceID, "Force": "true", "ForceStop": "true",
	}, &out)
}
