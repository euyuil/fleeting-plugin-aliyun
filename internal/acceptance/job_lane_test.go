package acceptance

import (
	"encoding/json"
	"strings"
	"testing"
)

func testLaneContext() jobLaneContext {
	c := jobLaneContext{Topology: jobLaneTopology, Repository: "example/project", ProjectID: "9", APIUserID: "7", RunID: "10", Attempt: "1",
		Candidate: strings.Repeat("a", 40), Manifest: strings.Repeat("b", 64), Configuration: strings.Repeat("c", 64),
		FixtureSHA: strings.Repeat("d", 40), Phase: "candidate", Profile: "full",
		Runners: map[string]jobLaneRunner{"instance": {ID: "5", Tag: "instance-lane"}, "docker-autoscaler": {ID: "6", Tag: "docker-lane"}}}
	c.FixtureRef = "acceptance/" + c.Candidate + "/" + c.RunID + "/" + c.Attempt
	return c
}

func testLaneVariables(t *testing.T, c jobLaneContext) []laneVariable {
	t.Helper()
	owner, err := c.ownership("docker-autoscaler", "manager-crash", "manager-crash", 1,
		strings.Repeat("e", 48), "r_000000000001", strings.Repeat("f", 48))
	if err != nil {
		t.Fatal(err)
	}
	values, err := signedLaneVariables(owner, "synthetic-existing-pat")
	if err != nil {
		t.Fatal(err)
	}
	var result []laneVariable
	for _, value := range values {
		result = append(result, laneVariable{Key: value["key"], Value: value["value"], VariableType: "env_var"})
	}
	return result
}

func TestLaneContextCannotBePartiallyOmittedOrRebound(t *testing.T) {
	c := testLaneContext()
	raw, _ := json.Marshal(c)
	var canonical map[string]any
	_ = json.Unmarshal(raw, &canonical)
	raw, _ = json.Marshal(canonical)
	env := map[string]string{"GITLAB_ACCEPTANCE_CONTEXT": string(raw), "ACCEPTANCE_EXECUTION_TOPOLOGY": jobLaneTopology,
		"GITHUB_REPOSITORY": c.Repository, "GITHUB_RUN_ID": c.RunID, "GITHUB_RUN_ATTEMPT": c.Attempt,
		"PLUGIN_COMMIT": c.Candidate, "ACCEPTANCE_MANIFEST_SHA256": c.Manifest, "GITLAB_CONFIG_SHA256": c.Configuration,
		"GITLAB_PROJECT_ID": c.ProjectID, "GITLAB_API_USER_ID": c.APIUserID, "GITLAB_FIXTURE_REF": c.FixtureRef, "GITLAB_FIXTURE_SHA": c.FixtureSHA,
		"ACCEPTANCE_PHASE": c.Phase, "ACCEPTANCE_VALIDATION_PROFILE": c.Profile,
		"GITLAB_EXPECTED_RUNNER_ID": c.Runners["instance"].ID, "GITLAB_RUNNER_TAG": c.Runners["instance"].Tag}
	get := func(k string) string { return env[k] }
	if scope, err := readJobLaneScope(get, "instance"); err != nil || scope == nil {
		t.Fatal(err)
	}
	for key, value := range env {
		t.Run(key, func(t *testing.T) {
			env[key] = ""
			defer func() { env[key] = value }()
			if _, err := readJobLaneScope(get, "instance"); err == nil {
				t.Fatal("partial lane binding was accepted")
			}
		})
	}
	if _, err := readJobLaneScope(get, "docker-autoscaler"); err == nil {
		t.Fatal("peer Runner was selected with local credentials")
	}
}

func TestLaneDeclarationsBindEveryScopeAndRejectOverrides(t *testing.T) {
	c := testLaneContext()
	variables := testLaneVariables(t, c)
	if _, err := c.verifyDeclaration(variables, "synthetic-existing-pat", false); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"topology", "repository", "project_id", "api_user_id", "run_id", "attempt", "candidate_sha", "manifest_sha256",
		"gitlab_config_sha256", "fixture_ref", "fixture_sha", "phase", "profile"} {
		t.Run(field, func(t *testing.T) {
			raw, _ := json.Marshal(c)
			var value map[string]any
			_ = json.Unmarshal(raw, &value)
			value[field] = "synthetic-other-scope"
			raw, _ = json.Marshal(value)
			var other jobLaneContext
			_ = json.Unmarshal(raw, &other)
			if _, err := other.verifyDeclaration(variables, "synthetic-existing-pat", false); err == nil {
				t.Fatal("a different acceptance scope passed")
			}
		})
	}
	for _, fault := range []string{"key", "signature", "override", "duplicate", "extra", "type", "missing", "noncanonical", "unknown-field"} {
		t.Run(fault, func(t *testing.T) {
			copy := append([]laneVariable(nil), variables...)
			key := "synthetic-existing-pat"
			switch fault {
			case "key":
				key = "synthetic-other-key"
			case "signature":
				copy[6].Value = strings.Repeat("0", 64)
			case "override":
				copy[0].Value = strings.Repeat("1", 48)
			case "duplicate":
				copy[1] = copy[0]
			case "extra":
				copy = append(copy, laneVariable{Key: "GIT_STRATEGY", Value: "none", VariableType: "env_var"})
			case "type":
				copy[0].VariableType = "file"
			case "missing":
				copy = copy[:len(copy)-1]
			case "noncanonical", "unknown-field":
				if fault == "noncanonical" {
					copy[5].Value = " " + copy[5].Value
				} else {
					copy[5].Value = strings.Replace(copy[5].Value, "{", `{"additional":"unexpected",`, 1)
				}
				copy[6].Value = laneSignature([]byte(copy[5].Value), key)
			}
			if _, err := c.verifyDeclaration(copy, key, false); err == nil {
				t.Fatal("invalid declaration passed")
			}
		})
	}
}

func TestLaneTopologyAndCaseIdentityAreExact(t *testing.T) {
	for _, fault := range []string{"extra-runner", "same-id", "same-tag", "missing-runner", "invalid-id"} {
		t.Run(fault, func(t *testing.T) {
			c := testLaneContext()
			switch fault {
			case "extra-runner":
				c.Runners["other"] = jobLaneRunner{ID: "7", Tag: "extra"}
			case "same-id":
				c.Runners["docker-autoscaler"] = jobLaneRunner{ID: "5", Tag: "docker-lane"}
			case "same-tag":
				c.Runners["docker-autoscaler"] = jobLaneRunner{ID: "6", Tag: "instance-lane"}
			case "missing-runner":
				delete(c.Runners, "docker-autoscaler")
			case "invalid-id":
				c.Runners["docker-autoscaler"] = jobLaneRunner{ID: "06", Tag: "docker-lane"}
			}
			if c.validate() == nil {
				t.Fatal("invalid two-Runner topology passed")
			}
		})
	}
	c := testLaneContext()
	for _, scenario := range []string{"unknown", "", "smoke/extra"} {
		if _, err := c.ownership("instance", scenario, "smoke", 2, strings.Repeat("e", 48), "r_000000000001", strings.Repeat("f", 48)); err == nil {
			t.Fatal("unknown case became a two-job sequence")
		}
	}
	c.Phase, c.Profile = "public", "public-smoke"
	prior := testLaneVariables(t, testLaneContext())
	if _, err := c.verifyDeclaration(prior, "synthetic-existing-pat", false); err == nil {
		t.Fatal("a prior candidate became current public work")
	}
	if _, err := c.verifyDeclaration(prior, "synthetic-existing-pat", true); err != nil {
		t.Fatal("recognized terminal candidate ownership cannot participate in replay detection", err)
	}
	if _, err := c.ownership("instance", "upgrade", "smoke", 1, strings.Repeat("e", 48), "r_000000000001", strings.Repeat("f", 48)); err == nil {
		t.Fatal("public-smoke admitted an upgrade case")
	}
}

func TestLaneJSONRejectsDuplicateKeysAndTrailingValues(t *testing.T) {
	for _, raw := range []string{`{"a":"x","a":"y"}`, `{"a":{"b":"x","b":"y"}}`, `{} {}`, `{"a":"x" }`} {
		var out any
		if decodeLaneJSON(raw, &out) == nil {
			t.Fatal("noncanonical or duplicate ownership JSON passed")
		}
	}
}
