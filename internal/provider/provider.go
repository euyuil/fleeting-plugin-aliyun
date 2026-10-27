// Package provider implements the Fleeting ESS instance group.
package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
	"github.com/hashicorp/go-hclog"
	upstream "gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

var _ upstream.InstanceGroup = (*InstanceGroup)(nil)

type capacityOperation struct {
	Target, Count int
	Token         string
	Reported      bool
	Prepared      bool
	Before        int
	CreatedAt     time.Time
}
type removalOperation struct {
	IDs               []string
	Token, Activity   string
	Accepted          []string
	Target            int
	Before            int
	CreatedAt         time.Time
	Prepared          bool
	AcceptedPersisted bool
	Recovered         bool
}
type InstanceGroup struct {
	Region             string         `json:"region"`
	ScalingGroupID     string         `json:"scaling_group_id"`
	SSHCredentials     string         `json:"ssh_credentials,omitempty"`
	OperationTimeout   string         `json:"operation_timeout,omitempty"`
	Client             *aliyun.Client `json:"-"`
	Version, BuildInfo string         `json:"-"`
	stateMu            sync.Mutex
	publishGate        chan struct{}
	gate               chan struct{}
	log                hclog.Logger
	settings           upstream.Settings
	timeout            time.Duration
	desired, max       int
	recoveredCredit    int
	capacity           *capacityOperation
	removal            *removalOperation
	known              map[string]upstream.State
	deleting           map[string]string
	closed             bool
	initialized        bool
}

func (g *InstanceGroup) UnmarshalJSON(data []byte) error {
	var config struct {
		Region           string `json:"region"`
		ScalingGroupID   string `json:"scaling_group_id"`
		OperationTimeout string `json:"operation_timeout"`
		SSHCredentials   string `json:"ssh_credentials"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&config); err != nil {
		return err
	}
	g.Region = config.Region
	g.ScalingGroupID = config.ScalingGroupID
	g.OperationTimeout = config.OperationTimeout
	g.SSHCredentials = config.SSHCredentials
	return nil
}
func (g *InstanceGroup) Init(ctx context.Context, log hclog.Logger, settings upstream.Settings) (upstream.ProviderInfo, error) {
	if g.initialized {
		return upstream.ProviderInfo{}, errors.New("provider is already initialized; start a new plugin process")
	}
	if strings.TrimSpace(g.Region) == "" || strings.TrimSpace(g.ScalingGroupID) == "" {
		return upstream.ProviderInfo{}, errors.New("region and scaling_group_id are required")
	}
	if strings.ContainsAny(g.Region, "./: \\ \t\n") {
		return upstream.ProviderInfo{}, errors.New("invalid region")
	}
	if g.SSHCredentials != "" && g.SSHCredentials != "static" && g.SSHCredentials != "cloud_assistant" {
		return upstream.ProviderInfo{}, errors.New("ssh_credentials must be static or cloud_assistant")
	}
	if g.SSHCredentials == "cloud_assistant" && settings.UseStaticCredentials {
		return upstream.ProviderInfo{}, errors.New("cloud_assistant requires use_static_credentials=false")
	}
	if g.SSHCredentials == "cloud_assistant" && ((settings.OS != "" && settings.OS != "linux") || (settings.Protocol != "" && settings.Protocol != upstream.ProtocolSSH) || !posixUser.MatchString(settings.Username)) {
		return upstream.ProviderInfo{}, errors.New("dynamic credentials require Linux SSH and a POSIX username")
	}
	validationSettings := settings.ConnectorConfig
	validationSettings.UseStaticCredentials = true
	if err := validateConnector(validationSettings); err != nil {
		return upstream.ProviderInfo{}, err
	}
	if g.SSHCredentials != "cloud_assistant" && !settings.UseStaticCredentials {
		return upstream.ProviderInfo{}, errors.New("static mode requires use_static_credentials=true")
	}
	g.timeout = 2 * time.Minute
	if g.OperationTimeout != "" {
		d, err := time.ParseDuration(g.OperationTimeout)
		if err != nil || d < time.Second || d > 10*time.Minute {
			return upstream.ProviderInfo{}, errors.New("operation_timeout must be between 1s and 10m")
		}
		g.timeout = d
	}
	if log == nil {
		log = hclog.NewNullLogger()
	}
	g.log = log
	g.gate = make(chan struct{}, 1)
	g.publishGate = make(chan struct{}, 1)
	g.known = map[string]upstream.State{}
	g.deleting = map[string]string{}
	g.settings = settings
	if g.Client == nil {
		api, err := newAPI(g.Region)
		if err != nil {
			return upstream.ProviderInfo{}, err
		}
		g.Client = &aliyun.Client{API: api, Region: g.Region, GroupID: g.ScalingGroupID}
	}
	if g.Client.Region != g.Region || g.Client.GroupID != g.ScalingGroupID {
		return upstream.ProviderInfo{}, errors.New("API client group binding does not match provider configuration")
	}
	g.Client.Log = g.log.With("region", g.Region, "scaling_group_id", g.ScalingGroupID)
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	group, err := g.Client.Group(ctx)
	if err != nil {
		return upstream.ProviderInfo{}, err
	}
	if err = g.Client.Validate(ctx, group); err != nil {
		return upstream.ProviderInfo{}, err
	}
	members, err := g.Client.Members(ctx)
	if err != nil {
		return upstream.ProviderInfo{}, err
	}
	for _, m := range members {
		if err = aliyun.ValidateMember(m); err != nil {
			return upstream.ProviderInfo{}, err
		}
	}
	visible := 0
	for id, member := range members {
		g.known[id] = upstream.StateCreating
		if strings.HasPrefix(member.LifecycleState, "Removing") {
			g.known[id] = upstream.StateDeleting
		} else {
			visible++
		}
	}
	g.desired, g.max = group.DesiredCapacity, group.MaxSize
	g.recoveredCredit = max(0, g.desired-visible)
	journal, err := aliyun.ReadJournal(group)
	if err != nil {
		return upstream.ProviderInfo{}, err
	}
	if journal != nil {
		if journal.Kind == "suspend" || journal.Kind == "resume" {
			return upstream.ProviderInfo{}, errors.New("experimental standby operation requires its isolated controller")
		}
		if journal.Kind == "increase" {
			g.capacity = &capacityOperation{Target: journal.Target, Count: journal.Count, Token: journal.Token, Reported: true, Prepared: true, Before: journal.Before, CreatedAt: journal.CreatedAt}
			g.recoveredCredit = max(0, journal.Target-visible)
		} else {
			op := &removalOperation{IDs: journal.IDs, Token: journal.Token, Activity: journal.Activity, Before: journal.Before, Target: journal.Target, CreatedAt: journal.CreatedAt, Prepared: true, AcceptedPersisted: journal.Activity != "", Recovered: true}
			for i, id := range op.IDs {
				if journal.AcceptedMask&(1<<i) != 0 {
					op.Accepted = append(op.Accepted, id)
					g.deleting[id] = op.Activity
				}
			}
			g.removal = op
			g.desired = journal.Before
			if journal.Activity != "" {
				g.recoveredCredit = max(0, journal.Target-g.visibleCapacity())
			}
		}
	}

	g.initialized = true
	return upstream.ProviderInfo{ID: "aliyun/" + g.Region + "/" + g.ScalingGroupID, MaxSize: g.max, Version: g.Version, BuildInfo: g.BuildInfo}, nil
}
func (g *InstanceGroup) enter(ctx context.Context) (context.Context, func(), error) {
	if g.gate == nil || !g.initialized {
		return nil, nil, errors.New("provider is not initialized")
	}
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	select {
	case g.gate <- struct{}{}:
		g.stateMu.Lock()
		closed := g.closed
		g.stateMu.Unlock()
		if closed {
			<-g.gate
			cancel()
			return nil, nil, errors.New("provider is shut down")
		}
		return ctx, func() { <-g.gate; cancel() }, nil
	case <-ctx.Done():
		cancel()
		return nil, nil, ctx.Err()
	}
}
func token() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func (g *InstanceGroup) confirmCapacity(ctx context.Context) error {
	if g.capacity == nil {
		return nil
	}
	op := g.capacity
	if !op.Prepared {
		if err := g.Client.StoreJournal(ctx, aliyun.Journal{Kind: "increase", Token: op.Token, Before: op.Before, Target: op.Target, Count: op.Count, CreatedAt: op.CreatedAt}); err != nil {
			return err
		}
		op.Prepared = true
	}
	current, err := g.Client.Group(ctx)
	if err != nil {
		return err
	}
	if current.MaxSize < op.Target || current.LifecycleState != "Active" {
		return errors.New("pending target is incompatible with current group limits or lifecycle")
	}
	if current.DesiredCapacity != op.Target {
		if current.DesiredCapacity > op.Target {
			return errors.New("capacity exceeds journal target")
		}
		if err = g.Client.SetCapacity(ctx, op.Target, op.Token); err != nil {
			return err
		}
	}

	for {
		s, err := g.Client.Group(ctx)
		if err != nil {
			return err
		}
		if s.DesiredCapacity == op.Target {
			g.desired = op.Target
			return nil
		}
		if s.DesiredCapacity > op.Target {
			return errors.New("desired capacity exceeded accepted intent; check competing writers")
		}
		if err = aliyun.Wait(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
}
func (g *InstanceGroup) Increase(parent context.Context, n int) (int, error) {
	if n < 0 {
		return 0, errors.New("increase count must not be negative")
	}
	if n == 0 {
		return 0, nil
	}
	ctx, done, err := g.enter(parent)
	if err != nil {
		return 0, err
	}
	defer done()
	if g.removal != nil {
		if err = g.finishRemoval(ctx); err != nil {
			return 0, err
		}
	}
	if g.capacity != nil {
		if err = g.confirmCapacity(ctx); err != nil {
			return 0, err
		}
		if !g.capacity.Reported {
			available := min(g.capacity.Count, max(0, g.capacity.Target-g.visibleCapacity()))
			count := min(n, available)
			remaining := available - count
			if remaining == 0 {
				if err = g.Client.ClearJournal(ctx); err != nil {
					return 0, err
				}
				g.capacity = nil
			} else {
				g.capacity.Count = remaining
			}
			if count > 0 {
				return count, nil
			}
		} else {
			if err = g.Client.ClearJournal(ctx); err != nil {
				return 0, err
			}
			g.capacity = nil
		}
	}
	group, err := g.Client.Group(ctx)
	if err != nil {
		return 0, err
	}
	if group.DesiredCapacity != g.desired {
		return 0, errors.New("desired capacity changed outside this provider")
	}
	if group.MaxSize != g.max || group.LifecycleState != "Active" {
		return 0, errors.New("group limits or state changed; restart after inspection")
	}
	g.recoveredCredit = min(g.recoveredCredit, max(0, g.desired-g.visibleCapacity()))
	if credit := min(n, g.recoveredCredit); credit > 0 {
		g.recoveredCredit -= credit
		return credit, nil
	}
	accepted := min(n, g.max-g.desired, 1000)
	if accepted <= 0 {
		return 0, nil
	}
	op := &capacityOperation{Target: g.desired + accepted, Count: accepted, Token: token(), CreatedAt: time.Now().UTC(), Before: g.desired}
	g.capacity = op
	if err = g.Client.StoreJournal(ctx, aliyun.Journal{Kind: "increase", Token: op.Token, Before: g.desired, Target: op.Target, Count: op.Count, CreatedAt: op.CreatedAt}); err != nil {
		return 0, err
	}
	op.Prepared = true
	g.capacity = op
	if err = g.Client.SetCapacity(ctx, op.Target, op.Token); err != nil {
		if !aliyun.IsAmbiguous(err) && !aliyun.IsRetryable(err) {
			if clearErr := g.Client.ClearJournal(ctx); clearErr != nil {
				return 0, clearErr
			}
			g.capacity = nil
		}
		return 0, err
	}
	op.Reported = true
	g.desired = op.Target
	g.log.Info("capacity request accepted", "target", op.Target, "accepted", accepted)
	return accepted, nil
}
func (g *InstanceGroup) visibleCapacity() int {
	g.stateMu.Lock()
	defer g.stateMu.Unlock()
	count := 0
	for id, state := range g.known {
		if g.deleting[id] == "" && (state == upstream.StateCreating || state == upstream.StateRunning) {
			count++
		}
	}
	return count
}

func (g *InstanceGroup) startRemoval(ctx context.Context) error {
	op := g.removal
	if !op.Prepared {
		if err := g.Client.StoreJournal(ctx, aliyun.Journal{Kind: "remove", Token: op.Token, Before: op.Before, Target: op.Before, IDs: op.IDs, CreatedAt: op.CreatedAt}); err != nil {
			return err
		}
		op.Prepared = true
	}
	if op.Activity == "" && time.Since(op.CreatedAt) > 30*time.Second {
		return errors.New("unresolved removal outcome exceeds retry window; inspect durable journal before recovery")
	}
	out, err := g.Client.Remove(ctx, op.IDs, op.Token)
	if err != nil {
		if !aliyun.IsAmbiguous(err) && !aliyun.IsRetryable(err) {
			if clearErr := g.Client.ClearJournal(ctx); clearErr != nil {
				return clearErr
			}
			g.removal = nil
		}
		return err
	}
	op.Activity = out.ScalingActivityId
	ignored := map[string]bool{}
	for _, i := range out.IgnoredInstances.IgnoredInstance {
		ignored[i.InstanceId] = true
	}
	op.Accepted = nil
	for _, id := range op.IDs {
		if !ignored[id] {
			op.Accepted = append(op.Accepted, id)
		}
	}
	if len(op.Accepted) > 0 && op.Activity == "" {
		return errors.New("removal accepted without an activity ID")
	}
	op.Target = op.Before - len(op.Accepted)
	if err = g.persistRemovalAcceptance(ctx); err != nil {
		return err
	}
	if len(op.Accepted) == 0 {
		if err = g.Client.ClearJournal(ctx); err != nil {
			return err
		}
		g.removal = nil
		return nil
	}
	g.log.Info("removal request accepted", "activity_id", op.Activity, "request_id", out.RequestId, "instance_ids", op.Accepted)
	return nil
}
func (g *InstanceGroup) persistRemovalAcceptance(ctx context.Context) error {
	op := g.removal
	if !op.AcceptedPersisted {
		var mask uint32
		for i, id := range op.IDs {
			if slices.Contains(op.Accepted, id) {
				mask |= 1 << i
			}
		}
		if err := g.Client.StoreJournal(ctx, aliyun.Journal{Kind: "remove", Token: op.Token, Before: op.Before, Target: op.Target, IDs: op.IDs, Activity: op.Activity, AcceptedMask: mask, CreatedAt: op.CreatedAt}); err != nil {
			return err
		}
		op.AcceptedPersisted = true
	}
	unlock, lockErr := g.lockPublish(ctx)
	if lockErr != nil {
		return lockErr
	}
	g.stateMu.Lock()
	for _, id := range op.Accepted {
		g.deleting[id] = op.Activity
	}
	g.stateMu.Unlock()
	unlock()

	return nil
}

func (g *InstanceGroup) finishRemoval(ctx context.Context) error {
	op := g.removal
	if op == nil {
		return nil
	}
	if op.Activity == "" {
		if err := g.startRemoval(ctx); err != nil {
			return err
		}
		if g.removal == nil {
			return nil
		}
	}
	if err := g.persistRemovalAcceptance(ctx); err != nil {
		return err
	}
	for {
		a, err := g.Client.Activity(ctx, op.Activity)
		if err != nil {
			return err
		}
		switch a.StatusCode {
		case "Successful", "Warning", "Failed", "Rejected":
			members, err := g.Client.Members(ctx)
			if err != nil {
				return err
			}
			instances, err := g.Client.Instances(ctx, op.Accepted)
			if err != nil {
				return err
			}
			residual := false
			for _, id := range op.Accepted {
				_, member := members[id]
				_, exists := instances[id]
				if member || exists {
					residual = true
				}
			}
			if residual && a.StatusCode == "Successful" {
				if err = aliyun.Wait(ctx, 100*time.Millisecond); err != nil {
					return err
				}
				continue
			}
			if residual {
				group, e := g.Client.Group(ctx)
				if e != nil {
					return e
				}
				released := 0
				for _, id := range op.Accepted {
					_, member := members[id]
					_, exists := instances[id]
					if !member && !exists {
						released++
					}
				}
				expected := op.Before - released
				g.stateMu.Lock()
				for _, id := range op.Accepted {
					_, member := members[id]
					_, exists := instances[id]
					if member || exists {
						delete(g.deleting, id)
					}
				}
				g.stateMu.Unlock()
				if group.DesiredCapacity == expected {
					if e = g.Client.ClearJournal(ctx); e != nil {
						return e
					}
					g.desired = expected
					if op.Recovered {
						g.recoveredCredit = max(0, expected-g.visibleCapacity())
					}
					g.removal = nil
					return fmt.Errorf("removal activity %s failed for remaining IDs; retry them", op.Activity)
				}
				return fmt.Errorf("removal activity %s has unverified partial capacity semantics; durable journal retained", op.Activity)
			}

			group, err := g.Client.Group(ctx)
			if err != nil {
				return err
			}
			if group.DesiredCapacity != op.Target {
				if err = aliyun.Wait(ctx, 100*time.Millisecond); err != nil {
					return err
				}
				continue
			}
			if err = g.Client.ClearJournal(ctx); err != nil {
				return err
			}
			g.desired = op.Target
			if op.Recovered {
				g.recoveredCredit = max(0, op.Target-g.visibleCapacity())
			}
			g.removal = nil
			return nil
		case "InProgress", "Pending":
		default:
			return fmt.Errorf("unknown scaling activity state %q", a.StatusCode)
		}
		if err = aliyun.Wait(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
}
func (g *InstanceGroup) Decrease(parent context.Context, ids []string) (succeededResult []string, resultErr error) {
	defer func() {
		if len(succeededResult) > 0 && resultErr != nil {
			g.log.Warn("partial deletion request accepted; remaining IDs will be retried", "error", resultErr)
			resultErr = nil
		}
	}()
	ctx, done, err := g.enter(parent)
	if err != nil {
		return nil, err
	}
	defer done()
	if g.capacity != nil {
		if err = g.confirmCapacity(ctx); err != nil {
			return nil, err
		}

		if err = g.Client.ClearJournal(ctx); err != nil {
			return nil, err
		}
		g.capacity = nil
	}
	if g.removal != nil {
		if err = g.finishRemoval(ctx); err != nil {
			return nil, err
		}
	}
	members, err := g.Client.Members(ctx)
	if err != nil {
		return nil, err
	}
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	var existing, absent, succeededRemoving []string
	for _, id := range ids {
		if m, ok := members[id]; ok {
			if err = aliyun.ValidateMember(m); err != nil {
				return nil, err
			}
			if strings.HasPrefix(m.LifecycleState, "Removing") {

				succeededRemoving = append(succeededRemoving, id)
				continue
			}
			existing = append(existing, id)
		} else {
			absent = append(absent, id)
		}
	}
	instances, err := g.Client.Instances(ctx, absent)
	if err != nil {
		return nil, err
	}
	succeeded := succeededRemoving
	for _, id := range absent {
		if _, exists := instances[id]; exists {
			return nil, fmt.Errorf("instance %s exists outside managed group; refusing release", id)
		}
		succeeded = append(succeeded, id)
	}
	for len(existing) > 0 {
		batch := slices.Clone(existing[:min(20, len(existing))])
		existing = existing[len(batch):]
		op := &removalOperation{IDs: batch, Token: token(), Before: g.desired, CreatedAt: time.Now().UTC()}
		g.removal = op
		if err = g.Client.StoreJournal(ctx, aliyun.Journal{Kind: "remove", Token: op.Token, Before: op.Before, Target: op.Before, IDs: op.IDs, CreatedAt: op.CreatedAt}); err != nil {
			return succeeded, err
		}
		op.Prepared = true
		g.removal = op
		if err = g.startRemoval(ctx); err != nil {
			return succeeded, err
		}
		if g.removal == nil {
			continue
		}
		succeeded = append(succeeded, g.removal.Accepted...)
		if len(existing) > 0 {
			if err = g.finishRemoval(ctx); err != nil {
				return succeeded, err
			}
		}
	}

	return succeeded, nil
}
func (g *InstanceGroup) readContext(parent context.Context) (context.Context, context.CancelFunc, error) {
	if g.gate == nil || !g.initialized {
		return nil, nil, errors.New("provider is not initialized")
	}
	g.stateMu.Lock()
	closed := g.closed
	g.stateMu.Unlock()
	if closed {
		return nil, nil, errors.New("provider is shut down")
	}
	ctx, cancel := context.WithTimeout(parent, g.timeout)
	return ctx, cancel, nil
}
func (g *InstanceGroup) snapshot(ctx context.Context) (map[string]MemberState, error) {
	g.stateMu.Lock()
	known := map[string]upstream.State{}
	for id, state := range g.known {
		known[id] = state
	}
	deleting := map[string]string{}
	for id, a := range g.deleting {
		deleting[id] = a
	}
	g.stateMu.Unlock()
	members, err := g.Client.Members(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(members)+len(known))
	for id := range members {
		ids = append(ids, id)
	}
	for id := range known {
		if _, ok := members[id]; !ok {
			ids = append(ids, id)
		}
	}
	instances, err := g.Client.Instances(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := map[string]MemberState{}
	for id, m := range members {
		if err = aliyun.ValidateMember(m); err != nil {
			return nil, err
		}
		i, exists := instances[id]
		state := upstream.StateCreating
		if activity := deleting[id]; activity != "" {
			a, e := g.Client.Activity(ctx, activity)
			if e != nil {
				return nil, e
			}
			if a.StatusCode == "Failed" || a.StatusCode == "Rejected" || a.StatusCode == "Warning" {
				delete(deleting, id)
				g.stateMu.Lock()
				delete(g.deleting, id)
				g.stateMu.Unlock()
			}
		}
		if deleting[id] != "" || strings.HasPrefix(m.LifecycleState, "Removing") {
			state = upstream.StateDeleting
		} else if m.LifecycleState == "InService" && exists && i.Status == "Running" && m.HealthStatus == "Healthy" {
			state = upstream.StateRunning
		}
		result[id] = MemberState{State: state, Member: m, Instance: i}
	}
	for id := range known {
		if _, ok := members[id]; !ok {
			if _, exists := instances[id]; exists {
				return nil, fmt.Errorf("managed instance %s left ESS but still exists in ECS", id)
			}
			result[id] = MemberState{State: upstream.StateDeleted}
		}
	}
	return result, nil
}

type MemberState struct {
	State    upstream.State
	Member   aliyun.Member
	Instance aliyun.Instance
}

func (g *InstanceGroup) Update(parent context.Context, fn func(string, upstream.State)) error {
	ctx, done, err := g.readContext(parent)
	if err != nil {
		return err
	}
	unlock, err := g.lockPublish(ctx)
	if err != nil {
		done()
		return err
	}
	defer unlock()
	snapshot, err := g.snapshot(ctx)
	if err != nil {
		done()
		return err
	}
	g.stateMu.Lock()
	for id, i := range snapshot {
		if i.State == upstream.StateDeleted {
			delete(g.known, id)
			delete(g.deleting, id)
		} else {
			g.known[id] = i.State
		}
	}
	g.stateMu.Unlock()
	done()
	ids := make([]string, 0, len(snapshot))
	for id := range snapshot {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		fn(id, snapshot[id].State)
	}
	return nil
}
func (g *InstanceGroup) Heartbeat(parent context.Context, id string) error {
	ctx, done, err := g.readContext(parent)
	if err != nil {
		return err
	}
	defer done()
	members, err := g.Client.Members(ctx)
	if err != nil {
		return err
	}
	instances, err := g.Client.Instances(ctx, []string{id})
	if err != nil {
		return err
	}
	m, member := members[id]
	i, exists := instances[id]
	if !member || !exists || m.HealthStatus == "Unhealthy" || i.Status == "Stopped" || i.Status == "Stopping" || strings.HasPrefix(m.LifecycleState, "Removing") {
		return upstream.ErrInstanceUnhealthy
	}
	if m.HealthStatus != "Healthy" || i.Status != "Running" {
		return errors.New("instance health is not yet known")
	}
	return nil
}
func (g *InstanceGroup) Suspend(context.Context, []string) ([]string, error) {
	return nil, upstream.ErrSuspendResumeNotSupported
}
func (g *InstanceGroup) Resume(context.Context, []string) ([]string, error) {
	return nil, upstream.ErrSuspendResumeNotSupported
}
func (g *InstanceGroup) Shutdown(ctx context.Context) error {
	_, done, err := g.enter(ctx)
	if err != nil {
		return err
	}
	defer done()
	g.stateMu.Lock()
	g.closed = true
	g.stateMu.Unlock()
	if closer, ok := g.Client.API.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func (g *InstanceGroup) lockPublish(ctx context.Context) (func(), error) {
	select {
	case g.publishGate <- struct{}{}:
		return func() { <-g.publishGate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
