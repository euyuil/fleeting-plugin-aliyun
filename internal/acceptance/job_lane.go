package acceptance

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const jobLaneTopology = "two-executor-lanes-v1"

type jobLaneRunner struct {
	ID  string `json:"runner_id"`
	Tag string `json:"runner_tag"`
}

// The full, shared configuration is authenticated independently of the lane
// selected for execution. None of this private ownership data is a receipt.
type jobLaneContext struct {
	Topology      string                   `json:"topology"`
	Repository    string                   `json:"repository"`
	ProjectID     string                   `json:"project_id"`
	APIUserID     string                   `json:"api_user_id"`
	RunID         string                   `json:"run_id"`
	Attempt       string                   `json:"attempt"`
	Candidate     string                   `json:"candidate_sha"`
	Manifest      string                   `json:"manifest_sha256"`
	Configuration string                   `json:"gitlab_config_sha256"`
	FixtureRef    string                   `json:"fixture_ref"`
	FixtureSHA    string                   `json:"fixture_sha"`
	Phase         string                   `json:"phase"`
	Profile       string                   `json:"profile"`
	Runners       map[string]jobLaneRunner `json:"runners"`
}

func readJobLaneScope(get func(string) string, executor string) (*jobLaneScope, error) {
	raw, topology := get("GITLAB_ACCEPTANCE_CONTEXT"), get("ACCEPTANCE_EXECUTION_TOPOLOGY")
	if raw == "" && topology == "" {
		return nil, nil // The standalone, single-Runner driver remains supported.
	}
	var c jobLaneContext
	if topology != jobLaneTopology {
		return nil, errors.New("unknown acceptance execution topology")
	}
	if err := decodeLaneJSON(raw, &c); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	runner, ok := c.Runners[executor]
	if !ok || c.Repository != get("GITHUB_REPOSITORY") || c.RunID != get("GITHUB_RUN_ID") || c.Attempt != get("GITHUB_RUN_ATTEMPT") ||
		c.Candidate != get("PLUGIN_COMMIT") || c.Manifest != get("ACCEPTANCE_MANIFEST_SHA256") ||
		c.Configuration != get("GITLAB_CONFIG_SHA256") || c.ProjectID != get("GITLAB_PROJECT_ID") || c.APIUserID != get("GITLAB_API_USER_ID") ||
		c.FixtureRef != get("GITLAB_FIXTURE_REF") || c.FixtureSHA != get("GITLAB_FIXTURE_SHA") ||
		c.Phase != get("ACCEPTANCE_PHASE") || c.Profile != get("ACCEPTANCE_VALIDATION_PROFILE") ||
		runner.ID != get("GITLAB_EXPECTED_RUNNER_ID") || runner.Tag != get("GITLAB_RUNNER_TAG") {
		return nil, errors.New("lane context differs from the requested candidate, configuration or Manager")
	}
	return &jobLaneScope{context: c, executor: executor}, nil
}

var laneHex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)
var laneHex48 = regexp.MustCompile(`^[0-9a-f]{48}$`)
var laneHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)
var laneTag = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var laneRepository = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var laneSystemID = regexp.MustCompile(`^[rs]_[A-Za-z0-9]{12,64}$`)

func lanePositiveID(value string) bool {
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

// A canonical representation also rejects duplicate JSON keys, unknown fields,
// whitespace variants and trailing values before this authenticated data is used.
func decodeLaneJSON(raw string, out any) error {
	if len(raw) > 8192 {
		return errors.New("lane ownership JSON exceeds its bound")
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return errors.New("lane ownership JSON is invalid")
	}
	canonical, err := json.Marshal(value)
	if err != nil || string(canonical) != raw {
		return errors.New("lane ownership JSON must be canonical and unique")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		return errors.New("lane ownership JSON fields are invalid")
	}
	return nil
}

func (c jobLaneContext) validate() error {
	if c.Topology != jobLaneTopology || !laneRepository.MatchString(c.Repository) ||
		!lanePositiveID(c.ProjectID) || !lanePositiveID(c.APIUserID) || !lanePositiveID(c.RunID) || !lanePositiveID(c.Attempt) ||
		!laneHex40.MatchString(c.Candidate) || !laneHex40.MatchString(c.FixtureSHA) ||
		!laneHex64.MatchString(c.Manifest) || !laneHex64.MatchString(c.Configuration) ||
		c.FixtureRef != "acceptance/"+c.Candidate+"/"+c.RunID+"/"+c.Attempt ||
		!(c.Phase == "candidate" && (c.Profile == "full" || c.Profile == "debug") || c.Phase == "public" && c.Profile == "public-smoke") {
		return errors.New("lane context does not identify an exact acceptance scope")
	}
	if len(c.Runners) != 2 {
		return errors.New("lane context requires exactly two executor Runners")
	}
	instance, docker := c.Runners["instance"], c.Runners["docker-autoscaler"]
	if !lanePositiveID(instance.ID) || !lanePositiveID(docker.ID) || instance.ID == docker.ID ||
		!laneTag.MatchString(instance.Tag) || !laneTag.MatchString(docker.Tag) || instance.Tag == docker.Tag {
		return errors.New("lane Runner identities and tags must be distinct and exact")
	}
	return nil
}

func (c jobLaneContext) ownership(executor, outerCase, innerCase string, index int, managerNonce, systemID, nonce string) (map[string]string, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	runner, ok := c.Runners[executor]
	cases := jobCases(outerCase)
	if !ok || !slices.Contains([]string{"smoke", "failure", "cancel", "timeout", "capacity", "drain", "worker-loss", "manager-crash", "upgrade"}, outerCase) ||
		index < 1 || index > len(cases) || cases[index-1] != innerCase ||
		(c.Profile == "public-smoke" && outerCase != "smoke") || !laneHex48.MatchString(managerNonce) ||
		!laneSystemID.MatchString(systemID) || !laneHex48.MatchString(nonce) {
		return nil, errors.New("lane ownership has an invalid case, Manager or job identity")
	}
	return map[string]string{
		"topology": c.Topology, "repository": c.Repository, "project_id": c.ProjectID, "api_user_id": c.APIUserID,
		"run_id": c.RunID, "attempt": c.Attempt, "candidate_sha": c.Candidate,
		"manifest_sha256": c.Manifest, "gitlab_config_sha256": c.Configuration,
		"fixture_ref": c.FixtureRef, "fixture_sha": c.FixtureSHA, "phase": c.Phase, "profile": c.Profile,
		"executor": executor, "acceptance_case": outerCase, "job_scenario": innerCase,
		"job_index": strconv.Itoa(index), "runner_id": runner.ID, "runner_tag": runner.Tag,
		"manager_nonce": managerNonce, "system_id": systemID, "nonce": nonce,
	}, nil
}

func laneSignature(raw []byte, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

func signedLaneVariables(owner map[string]string, key string) ([]map[string]string, error) {
	if key == "" {
		return nil, errors.New("lane signing requires the existing project credential")
	}
	raw, err := json.Marshal(owner)
	if err != nil {
		return nil, errors.New("cannot encode lane ownership")
	}
	return []map[string]string{
		{"key": "FLEETING_NONCE", "value": owner["nonce"]},
		{"key": "FLEETING_RUNNER_TAG", "value": owner["runner_tag"]},
		{"key": "FLEETING_EXPECTED_RUNNER_ID", "value": owner["runner_id"]},
		{"key": "FLEETING_EXECUTION_MODE", "value": owner["executor"]},
		{"key": "FLEETING_JOB_SCENARIO", "value": owner["job_scenario"]},
		{"key": "FLEETING_OWNERSHIP", "value": string(raw)},
		{"key": "FLEETING_OWNERSHIP_SIGNATURE", "value": laneSignature(raw, key)},
	}, nil
}

type laneVariable struct {
	Key          string `json:"key"`
	Value        string `json:"value"`
	VariableType string `json:"variable_type"`
}

// This verifies a declaration, not a server identity. The caller must still
// establish complete historical pipeline/nonce uniqueness and exact job IDs.
func (c jobLaneContext) verifyDeclaration(variables []laneVariable, key string, allowPriorPhase bool) (map[string]string, error) {
	if key == "" || len(variables) != 7 {
		return nil, errors.New("lane pipeline variable inventory is incomplete")
	}
	values := map[string]string{}
	for _, variable := range variables {
		if variable.VariableType != "env_var" || values[variable.Key] != "" || variable.Value == "" {
			return nil, errors.New("lane pipeline variables are duplicated or invalid")
		}
		values[variable.Key] = variable.Value
	}
	raw, signature := values["FLEETING_OWNERSHIP"], values["FLEETING_OWNERSHIP_SIGNATURE"]
	if !laneHex64.MatchString(signature) || !hmac.Equal([]byte(signature), []byte(laneSignature([]byte(raw), key))) {
		return nil, errors.New("lane pipeline ownership signature is invalid")
	}
	var owner map[string]string
	if err := decodeLaneJSON(raw, &owner); err != nil {
		return nil, err
	}
	context := c
	if allowPriorPhase && c.Phase == "public" && owner["phase"] == "candidate" && owner["profile"] == "full" {
		context.Phase, context.Profile = "candidate", "full"
	}
	index, _ := strconv.Atoi(owner["job_index"])
	expected, err := context.ownership(owner["executor"], owner["acceptance_case"], owner["job_scenario"], index,
		owner["manager_nonce"], owner["system_id"], owner["nonce"])
	if err != nil {
		return nil, err
	}
	want, _ := json.Marshal(expected)
	if !bytes.Equal(want, []byte(raw)) {
		return nil, errors.New("lane pipeline ownership belongs to a different scope")
	}
	expectedVariables, _ := signedLaneVariables(expected, key)
	for _, variable := range expectedVariables {
		if values[variable["key"]] != variable["value"] {
			return nil, errors.New("lane pipeline variable overrides its ownership")
		}
	}
	return owner, nil
}

func laneJobState(status string) (terminal, valid bool) {
	if slices.Contains([]string{"success", "failed", "canceled", "skipped"}, status) {
		return true, true
	}
	return false, slices.Contains([]string{"created", "preparing", "pending", "running", "canceling", "manual", "scheduled",
		"waiting_for_resource", "waiting_for_callback"}, status)
}
