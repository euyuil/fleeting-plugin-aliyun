package acceptance

import (
	"context"
	"errors"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

type crashSequence struct {
	jobSequence
	crash func(context.Context) (int, error)
}

func (s crashSequence) run(ctx context.Context) error {
	r := s.result
	first, err := s.start(ctx, "manager-crash")
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
		return errors.New("Manager crash requires one real running job")
	}
	pid, err := s.crash(ctx)
	if err != nil {
		return err
	}
	if pid <= 0 || pid == r.ManagerPID {
		return errors.New("Manager crash did not produce a distinct invocation")
	}
	first.CrashInjected = true
	r.ManagerPID, r.CrashVerified = pid, true
	// The killed Runner cannot upload a final job result. Wait for GitLab's
	// server-side timeout/drop; never cancel the job to manufacture recovery.
	if err := s.wait(ctx, first); err != nil {
		return err
	}
	if err := s.release(ctx); err != nil {
		return err
	}
	if len(r.Instances) != 1 {
		return errors.New("Manager crash created an unrequested replacement")
	}
	first.NormalRelease = true
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
