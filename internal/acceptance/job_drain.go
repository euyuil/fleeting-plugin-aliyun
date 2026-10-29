package acceptance

import (
	"context"
	"errors"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

type drainSequence struct {
	capacitySequence
	beginDrain func(context.Context) (<-chan error, error)
	drainState func(context.Context) (string, error)
	restart    func(context.Context) (int, error)
}

func (s drainSequence) run(ctx context.Context) error {
	r := s.result
	first, err := s.start(ctx, "drain")
	if err != nil {
		return err
	}
	for !first.Checkpoint {
		if err := s.checkManager(ctx); err != nil {
			return err
		}
		if err := s.observe(ctx); err != nil {
			return err
		}
		if err := s.poll(ctx, first); err != nil {
			return err
		}
		if err := aliyun.Wait(ctx, s.pollInterval); err != nil {
			return err
		}
	}
	if first.Status != "running" || len(r.Instances) != 1 {
		return errors.New("graceful drain requires one real running job")
	}
	drainCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	done, err := s.beginDrain(drainCtx)
	if err != nil {
		return err
	}
	released, stopped := false, false
	for {
		if err := s.observe(drainCtx); err != nil {
			return err
		}
		if !terminalJob(first.Status) {
			if err := s.poll(drainCtx, first); err != nil {
				return err
			}
		}
		state, err := s.drainState(drainCtx)
		if err != nil {
			return err
		}
		if !stopped {
			select {
			case err := <-done:
				if err != nil {
					return errors.New("manager stop command failed during the job")
				}
				stopped = true
			default:
			}
		}
		if !released {
			if first.Status != "running" || stopped || state == "stopped" {
				return errors.New("Manager stopped before the held job could finish")
			}
			if state == "draining" {
				if err := s.open(drainCtx, 0); err != nil {
					return err
				}
				released = true
			}
		} else {
			if state == "active" {
				return errors.New("Manager restarted during graceful drain")
			}
			if stopped && state == "stopped" && first.Status == "success" {
				break
			}
		}
		if err := aliyun.Wait(drainCtx, s.pollInterval); err != nil {
			return err
		}
	}
	if err := s.release(ctx); err != nil {
		return err
	}
	first.NormalRelease = true
	pid, err := s.restart(ctx)
	if err != nil {
		return err
	}
	if pid <= 0 || pid == r.ManagerPID {
		return errors.New("Manager did not start a distinct invocation after draining")
	}
	r.ManagerPID, r.DrainVerified = pid, true
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
