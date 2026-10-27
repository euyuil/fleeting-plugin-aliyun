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
		if err := Observe(ctx, c, l, typ, image, zone); err != nil {
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
			return errors.New("provider did not release the disposable worker within the normal release deadline")
		}
	}
}
