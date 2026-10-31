package acceptance

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

type jobLaneScope struct {
	context  jobLaneContext
	executor string
}

var errLaneVisibility = errors.New("owned lane pipeline has not materialized its exact job")

// All lists fit on one complete page: 34 full candidate pipelines plus two
// later public smoke pipelines. A further page is a failure, not a partial view.
func (g gitlab) laneList(ctx context.Context, path string, out any) error {
	page := gitlabPage{value: out}
	if err := g.request(ctx, "GET", path, nil, &page); err != nil {
		return err
	}
	if page.next {
		return errors.New("lane inventory exceeds its complete page bound")
	}
	return nil
}

func (g gitlab) verifyLaneFixture(ctx context.Context, runnerID int64, tag, token, systemID string) error {
	scope := g.lane
	c := scope.context
	if err := c.validate(); err != nil {
		return err
	}
	runner, ok := c.Runners[scope.executor]
	if !ok || runner.ID != strconv.FormatInt(runnerID, 10) || runner.Tag != tag || c.ProjectID != g.project || !laneSystemID.MatchString(systemID) {
		return errors.New("selected Manager does not belong to its exact executor lane")
	}
	var verified struct{ ID int64 }
	if err := g.requestAPI(ctx, "POST", "/runners/verify", map[string]string{"token": token, "system_id": systemID}, &verified); err != nil {
		return err
	}
	if verified.ID != runnerID {
		return errors.New("Runner token does not belong to its executor lane")
	}
	var available []struct{ ID int64 }
	if err := g.laneList(ctx, "/runners?per_page=100", &available); err != nil {
		return err
	}
	want := map[int64]bool{}
	for _, r := range c.Runners {
		id, _ := strconv.ParseInt(r.ID, 10, 64)
		want[id] = true
	}
	if len(available) != 2 || available[0].ID == available[1].ID || !want[available[0].ID] || !want[available[1].ID] {
		return errors.New("fixture project must expose exactly the two approved lane Runners")
	}
	for _, executor := range []string{"instance", "docker-autoscaler"} {
		pin := c.Runners[executor]
		var actual struct {
			ID          int64
			RunnerType  string   `json:"runner_type"`
			RunUntagged bool     `json:"run_untagged"`
			TagList     []string `json:"tag_list"`
			Locked      bool
			Paused      bool
			Projects    []struct{ ID int64 }
		}
		if err := g.requestAPI(ctx, "GET", "/runners/"+pin.ID, nil, &actual); err != nil {
			return err
		}
		if strconv.FormatInt(actual.ID, 10) != pin.ID || actual.RunnerType != "project_type" || actual.Paused || actual.RunUntagged ||
			!actual.Locked || len(actual.Projects) != 1 || strconv.FormatInt(actual.Projects[0].ID, 10) != c.ProjectID ||
			len(actual.TagList) != 1 || actual.TagList[0] != pin.Tag {
			return errors.New("each lane Runner must be locked to this project and its single distinct tag")
		}
	}
	// Retry only an authenticated pipeline whose job is not visible yet. Invalid
	// ownership never gets a grace allow; every reread shares this child deadline.
	_, err := g.readLaneInventory(ctx, nil)
	return err
}

type lanePipeline struct {
	pipeline pipeline
	job      job
	owner    map[string]string
}

func (g gitlab) readLaneInventory(ctx context.Context, own map[string]string) (map[int64]lanePipeline, error) {
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for {
		inventory := map[int64]lanePipeline{}
		err := g.inspectLaneInventory(readCtx, own, inventory)
		if !errors.Is(err, errLaneVisibility) {
			if err != nil {
				return nil, err
			}
			return inventory, err
		}
		if err := aliyun.Wait(readCtx, time.Second); err != nil {
			return nil, errLaneVisibility
		}
	}
}

var laneActiveStates = []string{"created", "waiting_for_resource", "preparing", "waiting_for_callback", "pending", "running", "canceling", "manual", "scheduled"}

// A case may admit only the peer lane. Cleanup additionally permits this exact
// local case and Manager; neither mode accepts a second active case per lane.
func (g gitlab) inspectLaneInventory(ctx context.Context, own map[string]string, inventory map[int64]lanePipeline) error {
	c := g.lane.context
	if err := g.rejectLaneChildren(ctx, "ref="+url.QueryEscape(c.FixtureRef)); err != nil {
		return err
	}
	var runs []pipeline
	if err := g.laneList(ctx, "/pipelines?ref="+url.QueryEscape(c.FixtureRef)+"&per_page=100", &runs); err != nil {
		return err
	}
	if len(runs) > 36 {
		return errors.New("run pipeline inventory exceeds both accepted phases")
	}
	owners := map[int64]map[string]string{}
	nonces, jobsSeen := map[string]bool{}, map[int64]int64{}
	slots, caseManagers, managerCases := map[string]bool{}, map[string]string{}, map[string]string{}
	activeCases := map[string]string{}
	activeCounts := map[string]int{}
	allowedActive := func(owner map[string]string) bool {
		if owner == nil || owner["phase"] != c.Phase || owner["profile"] != c.Profile {
			return false
		}
		if owner["executor"] != g.lane.executor {
			return true
		}
		return own != nil && owner["acceptance_case"] == own["acceptance_case"] &&
			owner["manager_nonce"] == own["manager_nonce"] && owner["system_id"] == own["system_id"]
	}
	for _, listed := range runs {
		if listed.ID <= 0 || owners[listed.ID] != nil || listed.Ref != c.FixtureRef || listed.SHA != c.FixtureSHA || listed.Source != "api" {
			return errors.New("run pipeline inventory has a duplicate or foreign identity")
		}
		var current pipeline
		if err := g.request(ctx, "GET", "/pipelines/"+strconv.FormatInt(listed.ID, 10), nil, &current); err != nil {
			return err
		}
		terminal, valid := laneJobState(current.Status)
		if !valid || current.ID != listed.ID || current.Ref != c.FixtureRef || current.SHA != c.FixtureSHA || current.Source != "api" ||
			strconv.FormatInt(current.ProjectID, 10) != c.ProjectID || strconv.FormatInt(current.User.ID, 10) != c.APIUserID {
			return errors.New("pipeline readback differs from its owning run, fixture or creator")
		}
		var variables []laneVariable
		if err := g.laneList(ctx, "/pipelines/"+strconv.FormatInt(current.ID, 10)+"/variables?per_page=100", &variables); err != nil {
			return err
		}
		if len(variables) == 0 && !terminal {
			return errLaneVisibility
		}
		owner, err := c.verifyDeclaration(variables, g.token, terminal)
		if err != nil {
			return err
		}
		if nonces[owner["nonce"]] {
			return errors.New("signed job nonce was replayed by another pipeline")
		}
		nonces[owner["nonce"]], owners[current.ID] = true, owner
		caseKey := owner["phase"] + "/" + owner["profile"] + "/" + owner["executor"] + "/" + owner["acceptance_case"]
		slot := caseKey + "/" + owner["job_index"]
		manager := owner["manager_nonce"] + "/" + owner["system_id"]
		if slots[slot] || caseManagers[caseKey] != "" && caseManagers[caseKey] != manager ||
			managerCases[owner["system_id"]] != "" && managerCases[owner["system_id"]] != caseKey {
			return errors.New("lane case reused a job slot or crossed Manager identities")
		}
		slots[slot], caseManagers[caseKey], managerCases[owner["system_id"]] = true, manager, caseKey
		var jobs []job
		if err := g.laneList(ctx, "/pipelines/"+strconv.FormatInt(current.ID, 10)+"/jobs?per_page=100&include_retried=true", &jobs); err != nil {
			return err
		}
		if len(jobs) == 0 {
			return errLaneVisibility
		}
		if len(jobs) != 1 || jobs[0].ID <= 0 || jobsSeen[jobs[0].ID] != 0 {
			return errors.New("lane pipeline lacks one exact unretried job and Manager identity")
		}
		if err := laneJobIdentity(jobs[0], current.ID, owner); err != nil {
			return err
		}
		jobsSeen[jobs[0].ID] = current.ID
		inventory[current.ID] = lanePipeline{pipeline: current, job: jobs[0], owner: owner}
		jobTerminal, valid := laneJobState(jobs[0].Status)
		if !valid || terminal != jobTerminal {
			return errLaneVisibility
		}
		if !terminal {
			if !allowedActive(owner) {
				return errors.New("executor lane still has unrelated nonterminal work")
			}
			if activeCases[owner["executor"]] != "" && activeCases[owner["executor"]] != caseKey {
				return errors.New("executor lane has more than one active acceptance case")
			}
			activeCases[owner["executor"]] = caseKey
			activeCounts[owner["executor"]]++
			if activeCounts[owner["executor"]] > 3 {
				return errors.New("lane exceeded its bounded job sequence")
			}
		}
	}
	// Keep a project-wide guard. Restricting reads to the run ref would conceal
	// unrelated work, including a nonterminal pipeline before its jobs appear.
	for _, status := range laneActiveStates {
		if err := g.rejectLaneChildren(ctx, "status="+status); err != nil {
			return err
		}
		var active []pipeline
		if err := g.laneList(ctx, "/pipelines?status="+status+"&per_page=100", &active); err != nil {
			return err
		}
		if len(active) > 6 {
			return errors.New("project contains more active pipelines than two cases can own")
		}
		for _, p := range active {
			owner := owners[p.ID]
			if owner == nil && p.Ref == c.FixtureRef && p.SHA == c.FixtureSHA && p.Source == "api" {
				return errLaneVisibility
			}
			if !allowedActive(owner) || p.Ref != c.FixtureRef || p.SHA != c.FixtureSHA || p.Source != "api" {
				return errors.New("project contains an unrelated or stale nonterminal pipeline")
			}
			terminal, valid := laneJobState(p.Status)
			if !valid {
				return errors.New("project pipeline has an unknown state")
			}
			if terminal || terminalJob(inventory[p.ID].pipeline.Status) {
				return errLaneVisibility
			}
		}
	}
	query := url.Values{"per_page": {"100"}, "scope[]": laneActiveStates}
	var activeJobs []job
	if err := g.laneList(ctx, "/jobs?"+query.Encode(), &activeJobs); err != nil {
		return err
	}
	if len(activeJobs) > 6 {
		return errors.New("project contains more active jobs than two cases can own")
	}
	for _, j := range activeJobs {
		owner := owners[j.Pipeline.ID]
		if !allowedActive(owner) || jobsSeen[j.ID] != j.Pipeline.ID || j.Pipeline.ID <= 0 {
			return errors.New("project contains an unrelated or stale nonterminal job")
		}
		if err := laneJobIdentity(j, j.Pipeline.ID, owner); err != nil {
			return err
		}
		if terminalJob(j.Status) || terminalJob(inventory[j.Pipeline.ID].job.Status) || terminalJob(inventory[j.Pipeline.ID].pipeline.Status) {
			return errLaneVisibility
		}
	}
	return nil
}

func (g gitlab) rejectLaneChildren(ctx context.Context, selector string) error {
	// The default GitLab pipeline listing excludes children. Read that omitted
	// source separately; this API-only fixture can never own a child pipeline.
	var children []pipeline
	if err := g.laneList(ctx, "/pipelines?"+selector+"&source=parent_pipeline&per_page=100", &children); err != nil {
		return err
	}
	if len(children) != 0 {
		return errors.New("child pipelines are outside the acceptance ownership contract")
	}
	return nil
}

func laneJobIdentity(j job, pipelineID int64, owner map[string]string) error {
	terminal, valid := laneJobState(j.Status)
	if !valid {
		return errors.New("lane job has an unknown state")
	}
	if j.Pipeline.ID != pipelineID || j.Commit.ID != owner["fixture_sha"] || j.Ref != owner["fixture_ref"] ||
		len(j.TagList) != 1 || j.TagList[0] != owner["runner_tag"] {
		return errors.New("lane job differs from its pipeline, fixture or tag")
	}
	name := "spot-smoke"
	if owner["job_scenario"] == "manager-crash" {
		name = "spot-manager-crash"
	} else if slices.Contains([]string{"capacity", "drain", "worker-loss"}, owner["job_scenario"]) {
		name = "spot-controlled"
	}
	if j.Name != name {
		return errors.New("lane job differs from its exact scenario")
	}
	if j.Runner.ID == 0 {
		if j.RunnerManager.SystemID == "" && slices.Contains([]string{"created", "pending", "manual", "scheduled", "waiting_for_resource", "waiting_for_callback", "canceled", "skipped"}, j.Status) {
			return nil
		}
		return errors.New("unassigned lane job has an invalid Manager or outcome")
	}
	if strconv.FormatInt(j.Runner.ID, 10) != owner["runner_id"] {
		return errors.New("lane job belongs to a different Runner")
	}
	if j.RunnerManager.SystemID == "" && !terminal {
		return errLaneVisibility
	}
	if j.RunnerManager.SystemID != owner["system_id"] {
		return errors.New("lane job belongs to a different Manager")
	}
	return nil
}
