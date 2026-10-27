// Package acceptance implements explicitly enabled live validation and recovery.
package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

const ProjectTag = "fleeting-project"
const RunTag = "fleeting-run"
const ExpiryTag = "fleeting-expires-at"

type Resource struct {
	InstanceID          string   `json:"instance_id"`
	Origin              string   `json:"creation_origin"`
	Zone                string   `json:"zone"`
	InstanceType        string   `json:"instance_type"`
	SpotVerified        bool     `json:"spot_verified"`
	UnsafeDiskPolicy    bool     `json:"unsafe_disk_policy"`
	InventoryComplete   bool     `json:"inventory_complete"`
	AutoReleaseAt       string   `json:"auto_release_at,omitempty"`
	DeletionVerified    bool     `json:"deletion_verified"`
	DiskIDs             []string `json:"disk_ids"`
	NetworkInterfaceIDs []string `json:"network_interface_ids"`
}
type Ledger struct {
	TemplateID           string     `json:"launch_template_id"`
	TemplateVersion      string     `json:"launch_template_version"`
	ImageID              string     `json:"image_id"`
	SpotStrategy         string     `json:"spot_strategy"`
	InstanceLimit        int        `json:"instance_limit"`
	CreationBudget       int        `json:"creation_budget"`
	DeadlineAt           time.Time  `json:"deadline_at"`
	SpotPriceLimit       float64    `json:"spot_price_limit"`
	PublicBandwidthMbps  int        `json:"public_bandwidth_mbps"`
	SystemDiskSizeGiB    int        `json:"system_disk_size_gib"`
	SystemDiskCategory   string     `json:"system_disk_category"`
	Mode                 string     `json:"mode"`
	SchemaVersion        int        `json:"schema_version"`
	BaselineActivities   []string   `json:"baseline_activity_ids"`
	HistoryBaselineKnown bool       `json:"history_baseline_known"`
	EvidenceIncomplete   bool       `json:"evidence_incomplete"`
	SecurityGroupID      string     `json:"security_group_id,omitempty"`
	IngressCIDR          string     `json:"ingress_cidr,omitempty"`
	DriverPID            int        `json:"driver_pid,omitempty"`
	ManagerPID           int        `json:"manager_pid,omitempty"`
	ManagerDirectory     string     `json:"manager_directory,omitempty"`
	Path                 string     `json:"-"`
	Project              string     `json:"project"`
	Region               string     `json:"region"`
	GroupID              string     `json:"scaling_group_id"`
	RunID                string     `json:"github_run_id"`
	Attempt              string     `json:"github_run_attempt"`
	StartedAt            time.Time  `json:"started_at"`
	ExpiresAt            time.Time  `json:"expires_at"`
	Instances            []Resource `json:"instances"`
	CleanupResult        string     `json:"cleanup_result"`
	Leftovers            []string   `json:"leftover_resources"`
}

func (l *Ledger) Save() error {
	if l.Path == "" {
		return nil
	}
	return WriteJSON(l.Path, l)
}

func (l *Ledger) Marker() string { return l.RunID + "/" + l.Attempt }
func WriteJSON(path string, value any) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".evidence-")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err = file.Write(append(b, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(temporary, path); err != nil {
		return err
	}
	folder, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer folder.Close()
	return folder.Sync()
}

func tags(ctx context.Context, c *aliyun.Client, values map[string]string) error {
	q := map[string]string{"RegionId": c.Region, "ResourceType": "scalinggroup", "ResourceId.1": c.GroupID}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for i, k := range keys {
		q[fmt.Sprintf("Tag.%d.Key", i+1)] = k
		q[fmt.Sprintf("Tag.%d.Value", i+1)] = values[k]
	}
	var out struct{ RequestId string }
	return c.API.Call(ctx, "ess", "TagResources", q, &out)
}
func Claim(ctx context.Context, c *aliyun.Client, l *Ledger) error {
	if l.Mode == "" {
		l.Mode = "smoke"
	}
	g, err := c.Group(ctx)
	if err != nil {
		return err
	}
	if g.Tag(ProjectTag) != l.Project || g.Tag(RunTag) != "" {
		return errors.New("pool ownership mismatch or previous run marker present")
	}
	members, err := c.Members(ctx)
	if err != nil {
		return err
	}
	if len(members) != 0 || g.DesiredCapacity != 0 || g.TotalCapacity != 0 {
		return errors.New("pool is occupied")
	}
	baseline, e := History(ctx, c)
	if e != nil {
		return e
	}
	l.HistoryBaselineKnown = true
	for _, a := range baseline {
		if a.StatusCode == "InProgress" {
			return errors.New("pool has an active scaling activity")
		}
		l.BaselineActivities = append(l.BaselineActivities, a.ScalingActivityId)
	}
	if e = l.Save(); e != nil {
		return e
	}
	if err = tags(ctx, c, map[string]string{"fleeting-started-at": l.StartedAt.UTC().Format(time.RFC3339), "fleeting-baseline": baselineDigest(l.BaselineActivities), "fleeting-mode": l.Mode, RunTag: l.Marker(), ExpiryTag: l.ExpiresAt.UTC().Format(time.RFC3339)}); err != nil {
		return err
	}
	g, err = c.Group(ctx)
	if err != nil {
		return err
	}
	if g.Tag(RunTag) != l.Marker() {
		return errors.New("run marker did not persist")
	}
	return nil
}

// Cleanup requires a stopped Manager and an exact independently configured pool.
// Janitors additionally require an expired marker. A foreign or active run fails closed.
func Cleanup(ctx context.Context, c *aliyun.Client, l *Ledger, expiredOnly bool) error {
	l.CleanupResult = "CLEANUP_FAILED"
	g, err := c.Group(ctx)
	if err != nil {
		return err
	}
	if l.Region != c.Region || l.GroupID != c.GroupID || g.Tag(ProjectTag) != l.Project {
		return errors.New("cleanup ownership mismatch")
	}
	marker := g.Tag(RunTag)
	if marker == "" {
		if g.DesiredCapacity == 0 && g.TotalCapacity == 0 {
			members, e := c.Members(ctx)
			if e != nil {
				return e
			}
			if len(members) > 0 {
				return errors.New("unmarked pool still has members")
			}
			ids := []string{}
			for _, r := range l.Instances {
				ids = append(ids, r.InstanceID)
			}
			instances, e := c.Instances(ctx, ids)
			if e != nil {
				return e
			}
			if len(instances) > 0 {
				return errors.New("previous run still has ECS resources")
			}
			if err := verifyAssociated(ctx, c, l); err != nil {
				return err
			}
			if l.EvidenceIncomplete {
				return errors.New("previous cleanup inventory remains unverified")
			}
			l.CleanupResult = "PASS"
			return nil
		}
		return errors.New("cleanup run marker is missing")
	}
	if marker != l.Marker() {
		return errors.New("another run owns the pool")
	}
	expiry, err := time.Parse(time.RFC3339, g.Tag(ExpiryTag))
	if err != nil {
		return errors.New("invalid cloud expiry marker")
	}
	if expiredOnly && time.Now().Before(expiry) {
		return errors.New("run has not expired")
	}
	if g.MinSize != 0 || g.ScalingPolicy != "release" || g.LifecycleState != "Active" {
		return errors.New("cleanup requires an Active release-policy group with MinSize=0")
	}
	members, err := c.Members(ctx)
	if err != nil {
		return err
	}
	for id, m := range members {
		if err = validateCleanupMember(m, l.Mode, g.Tag("fleeting-mode")); err != nil {
			return err
		}
		if !slices.ContainsFunc(l.Instances, func(r Resource) bool { return r.InstanceID == id }) {
			l.Instances = append(l.Instances, Resource{InstanceID: id, Origin: "AutoCreated in exclusively claimed group"})
		}
	}
	inventoryErr := RecoverResources(ctx, c, l)
	if inventoryErr != nil {
		l.EvidenceIncomplete = true
	}
	if err = l.Save(); err != nil {
		l.EvidenceIncomplete = true
	}
	// Every member in this exclusively owned test group is disposable. Zeroing
	// intent also cancels pending creation, which specified-member deletion cannot.
	if err = c.SetCapacity(ctx, 0, "cleanup-"+l.RunID+"-"+l.Attempt); err != nil {
		return err
	}
	stable := 0
	for {
		g, err = c.Group(ctx)
		if err != nil {
			return err
		}
		if g.Tag(RunTag) != l.Marker() {
			return errors.New("ownership changed during cleanup")
		}
		members, err = c.Members(ctx)
		if err != nil {
			return err
		}
		for id, m := range members {
			if err = validateCleanupMember(m, l.Mode, g.Tag("fleeting-mode")); err != nil {
				return err
			}
			if !slices.ContainsFunc(l.Instances, func(r Resource) bool { return r.InstanceID == id }) {
				l.Instances = append(l.Instances, Resource{InstanceID: id, Origin: "AutoCreated in exclusively claimed group"})
			}
		}
		if e := RecoverResources(ctx, c, l); e != nil {
			l.EvidenceIncomplete = true
		}
		if g.DesiredCapacity == 0 && len(members) > 0 {
			batch := []string{}
			for id, m := range members {
				if m.LifecycleState == "InService" || m.LifecycleState == "Standby" {
					batch = append(batch, id)
				}
			}
			slices.Sort(batch)
			for len(batch) > 0 {
				selected := batch[:min(20, len(batch))]
				batch = batch[len(selected):]
				sum := sha256.Sum256([]byte(l.Marker() + time.Now().UTC().Format(time.RFC3339Nano)))
				out, e := c.RemoveWithDesired(ctx, selected, hex.EncodeToString(sum[:]), false)
				if e != nil {
					if !aliyun.IsRetryable(e) {
						return e
					}
				} else if out.ScalingActivityId != "" {
					if _, e = c.Activity(ctx, out.ScalingActivityId); e != nil {
						return e
					}
				}
			}
		}

		ids := make([]string, 0, len(l.Instances))
		for _, r := range l.Instances {
			ids = append(ids, r.InstanceID)
		}
		instances, err := c.Instances(ctx, ids)
		if err != nil {
			return err
		}
		l.Leftovers = nil
		for n, r := range l.Instances {
			_, exists := instances[r.InstanceID]
			l.Instances[n].DeletionVerified = !exists
			if exists {
				l.Leftovers = append(l.Leftovers, r.InstanceID)
			}
		}
		quiet, e := historyQuiet(ctx, c)
		if e != nil {
			l.EvidenceIncomplete = true
		}
		if e == nil && quiet && len(members) == 0 && len(instances) == 0 && g.DesiredCapacity == 0 && g.TotalCapacity == 0 && g.PendingCapacity == 0 && g.RemovingCapacity == 0 {
			stable++
		} else {
			stable = 0
		}
		if err = l.Save(); err != nil {
			return err
		}
		if stable >= 2 {
			break
		}
		if err = aliyun.Wait(ctx, time.Second); err != nil {
			return err
		}
	}
	if err = RecoverResources(ctx, c, l); err != nil {
		return err
	}
	associatedErr := verifyAssociated(ctx, c, l)
	if err = RemoveIngress(ctx, c, l); err != nil {
		return err
	}
	if associatedErr != nil {
		return associatedErr
	}

	if len(l.Leftovers) > 0 {
		return errors.New("associated resources remain; cleanup is incomplete")
	}

	if l.EvidenceIncomplete {
		return errors.New("resource inventory is incomplete; release cannot be certified")
	}
	if err = c.ClearJournal(ctx); err != nil {
		return err
	}
	if err = tags(ctx, c, map[string]string{RunTag: "", ExpiryTag: "", "fleeting-started-at": "", "fleeting-baseline": "", "fleeting-mode": ""}); err != nil {
		return err
	}

	l.CleanupResult = "PASS"
	return nil
}

func verifyAssociated(ctx context.Context, c *aliyun.Client, l *Ledger) error {
	var err error

	for _, r := range l.Instances {
		if !r.InventoryComplete {
			l.EvidenceIncomplete = true
		}
		for _, disk := range r.DiskIDs {
			if disk == "" {
				return errors.New("missing disk identity in ledger")
			}
			var out struct{ TotalCount *int }
			ids, _ := json.Marshal([]string{disk})
			if err = c.API.Call(ctx, "ecs", "DescribeDisks", map[string]string{"RegionId": c.Region, "DiskIds": string(ids)}, &out); err != nil {
				return err
			}
			if out.TotalCount == nil || *out.TotalCount != 0 {
				l.Leftovers = append(l.Leftovers, disk)
			}
		}
		for _, eni := range r.NetworkInterfaceIDs {
			if eni == "" {
				return errors.New("missing ENI identity in ledger")
			}
			var out struct{ TotalCount *int }
			if err = c.API.Call(ctx, "ecs", "DescribeNetworkInterfaces", map[string]string{"RegionId": c.Region, "NetworkInterfaceId.1": eni}, &out); err != nil {
				return err
			}
			if out.TotalCount == nil || *out.TotalCount != 0 {
				l.Leftovers = append(l.Leftovers, eni)
			}
		}
	}
	if len(l.Leftovers) > 0 {
		return errors.New("associated resources remain")
	}
	return nil
}

func validateCleanupMember(m aliyun.Member, ledgerMode, cloudMode string) error {
	if ledgerMode == "suspend" && cloudMode == "suspend" && m.LifecycleState == "Standby" {
		m.LifecycleState = "InService"
	}
	return aliyun.ValidateTestMember(m)
}
