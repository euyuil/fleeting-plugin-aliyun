package acceptance

import (
	"context"
	"errors"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

func WaitForNormalRelease(parent context.Context, c *aliyun.Client, l *Ledger, typ, image, zone string) error {
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	stable := 0
	for {
		if err := ObserveBudget(ctx, c, l, max(1, l.CreationBudget), typ, image, zone); err != nil {
			return err
		}
		if err := l.Save(); err != nil {
			return errors.New("cannot persist release evidence")
		}
		g, err := c.Group(ctx)
		if err != nil {
			return err
		}
		members, err := c.Members(ctx)
		if err != nil {
			return err
		}
		ids := []string{}
		for _, r := range l.Instances {
			ids = append(ids, r.InstanceID)
		}
		instances, err := c.Instances(ctx, ids)
		if err != nil {
			return err
		}
		quiet, err := historyQuiet(ctx, c)
		if err != nil {
			return err
		}
		if quiet && g.DesiredCapacity == 0 && g.TotalCapacity == 0 && g.PendingCapacity == 0 && g.RemovingCapacity == 0 && len(members) == 0 && len(instances) == 0 {
			stable++
		} else {
			stable = 0
		}
		if stable >= 2 {
			if l.EvidenceIncomplete {
				return errors.New("normal release resource evidence is incomplete")
			}
			if err = verifyAssociated(ctx, c, l); err != nil {
				return err
			}
			for i := range l.Instances {
				l.Instances[i].DeletionVerified = true
			}
			return nil
		}
		if err = aliyun.Wait(ctx, time.Second); err != nil {
			if parent.Err() != nil {
				return parent.Err()
			}
			return productViolation("provider did not release the disposable worker within the normal release deadline")
		}
	}
}

// WaitForSpecifiedRelease observes the two-worker scale-in without deleting
// anything. The same retained identity must survive throughout the observation.
func WaitForSpecifiedRelease(parent context.Context, c *aliyun.Client, l *Ledger, removed, retained, typ, image, zone string) error {
	if removed == "" || retained == "" || removed == retained || l.CreationBudget != 2 || len(l.Instances) != 2 || l.EvidenceIncomplete {
		return errors.New("specified release requires two complete, distinct worker identities")
	}
	index := -1
	foundRetained := false
	for i, r := range l.Instances {
		if !r.InventoryComplete || !r.SpotVerified {
			return errors.New("specified release resource evidence is incomplete")
		}
		if r.InstanceID == removed {
			index = i
		}
		if r.InstanceID == retained {
			foundRetained = true
		}
	}
	if index < 0 || !foundRetained {
		return errors.New("specified release identities are not in the resource ledger")
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	stable := 0
	for {
		if err := ObserveBudget(ctx, c, l, 2, typ, image, zone); err != nil {
			return err
		}
		if l.EvidenceIncomplete {
			return errors.New("specified release resource evidence became incomplete")
		}
		if err := l.Save(); err != nil {
			return err
		}
		g, err := c.Group(ctx)
		if err != nil {
			return err
		}
		members, err := c.Members(ctx)
		if err != nil {
			return err
		}
		instances, err := c.Instances(ctx, []string{removed, retained})
		if err != nil {
			return err
		}
		keepMember, memberOK := members[retained]
		keepInstance, instanceOK := instances[retained]
		if !memberOK || !instanceOK {
			return errors.New("specified scale-in removed the retained worker")
		}
		quiet, err := historyQuiet(ctx, c)
		if err != nil {
			return err
		}
		if quiet && g.DesiredCapacity == 1 && g.TotalCapacity == 1 && g.PendingCapacity == 0 && g.RemovingCapacity == 0 && len(members) == 1 && len(instances) == 1 && keepMember.LifecycleState == "InService" && keepInstance.Status == "Running" {
			stable++
		} else {
			stable = 0
		}
		if stable >= 2 {
			onlyRemoved := *l
			onlyRemoved.Instances = []Resource{l.Instances[index]}
			onlyRemoved.Leftovers = nil
			if err = verifyAssociated(ctx, c, &onlyRemoved); err != nil {
				return err
			}
			l.Instances[index].DeletionVerified = true
			return l.Save()
		}
		if err = aliyun.Wait(ctx, time.Second); err != nil {
			return errors.New("specified scale-in did not reach one retained worker before the deadline")
		}
	}
}
