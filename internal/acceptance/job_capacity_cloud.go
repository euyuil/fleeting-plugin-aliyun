package acceptance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
	"gitlab.com/gitlab-org/fleeting/fleeting/connector"
	upstream "gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

func checkJobCapacity(ctx context.Context, cloud *aliyun.Client) error {
	g, err := cloud.Group(ctx)
	if err != nil {
		return err
	}
	if g.MaxSize != 2 || g.DesiredCapacity < 0 || g.DesiredCapacity > 2 || g.TotalCapacity < 0 || g.TotalCapacity > 2 || g.PendingCapacity > 2 || g.RemovingCapacity > 2 {
		return errors.New("job fixture exceeded its two-worker capacity")
	}
	return nil
}

func normalWorkerGone(ctx context.Context, cloud *aliyun.Client, ledger *Ledger, id string) (bool, error) {
	members, err := cloud.Members(ctx)
	if err != nil {
		return false, err
	}
	instances, err := cloud.Instances(ctx, []string{id})
	if err != nil {
		return false, err
	}
	if _, exists := members[id]; exists || len(instances) != 0 {
		return false, nil
	}
	for i, resource := range ledger.Instances {
		if resource.InstanceID != id {
			continue
		}
		if !resource.InventoryComplete || !resource.SpotVerified || ledger.EvidenceIncomplete {
			return false, errors.New("released job worker lacks complete inventory")
		}
		only := *ledger
		only.Instances, only.Leftovers = []Resource{resource}, nil
		if err := verifyAssociated(ctx, cloud, &only); err != nil {
			if len(only.Leftovers) > 0 {
				return false, nil
			}
			return false, err
		}
		ledger.Instances[i].DeletionVerified = true
		return true, ledger.Save()
	}
	return false, errors.New("released job worker is absent from the ledger")
}

func capacityCommand(e JobEvidence, executor string) (string, error) {
	if !regexp.MustCompile(`^[0-9a-f]{48}$`).MatchString(e.Nonce) || e.JobID <= 0 || !e.Checkpoint {
		return "", errors.New("capacity control requires a bound checkpoint")
	}
	command := fmt.Sprintf(`test "$(cat /tmp/fleeting-acceptance-isolation)" = "%s" && touch /tmp/fleeting-acceptance-release-%s && printf %s`, e.Nonce, e.Nonce, e.Nonce)
	switch executor {
	case "instance":
		return command, nil
	case "docker-autoscaler":
		// Runner's pinned version labels containers with their concrete job ID.
		// No services are configured; require exactly one running job container.
		return fmt.Sprintf(`set -eu; set -- $(docker ps --filter label=com.gitlab.gitlab-runner.job.id=%d --format '{{.ID}}'); test "$#" = 1; docker exec "$1" sh -c '%s'`, e.JobID, command), nil
	default:
		return "", errors.New("unsupported capacity executor")
	}
}

func openCapacityJob(parent context.Context, cloud *aliyun.Client, e JobEvidence, executor, user string, key []byte) error {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	command, err := capacityCommand(e, executor)
	if err != nil {
		return err
	}
	members, err := cloud.Members(ctx)
	if err != nil {
		return err
	}
	member, exists := members[e.InstanceID]
	if !exists || member.LifecycleState != "InService" {
		return errors.New("controlled worker is not an active fixture member")
	}
	instances, err := cloud.Instances(ctx, []string{e.InstanceID})
	if err != nil {
		return err
	}
	worker, exists := instances[e.InstanceID]
	if !exists || worker.Status != "Running" || len(worker.PublicIpAddress.IpAddress) != 1 || net.ParseIP(worker.PublicIpAddress.IpAddress[0]) == nil {
		return errors.New("controlled worker has no unique running SSH endpoint")
	}
	info := upstream.ConnectInfo{ID: e.InstanceID, ExternalAddr: worker.PublicIpAddress.IpAddress[0],
		ConnectorConfig: upstream.ConnectorConfig{Username: user, Key: key, UseStaticCredentials: true,
			OS: "linux", Arch: "amd64", Protocol: upstream.ProtocolSSH, ProtocolPort: 22, Timeout: 20 * time.Second}}
	var output bytes.Buffer
	err = connector.Run(ctx, info, connector.ConnectorOptions{DialOptions: connector.DialOptions{UseExternalAddr: true},
		RunOptions: connector.RunOptions{Command: command, Stdout: &output}})
	if err != nil || output.String() != e.Nonce {
		return errors.New("capacity job barrier did not acknowledge its nonce")
	}
	return nil
}
