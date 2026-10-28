// Package manager configures a dedicated local Runner service without cloud calls.
package manager

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

var environmentName = regexp.MustCompile(`^ALIBABA_CLOUD_[A-Z0-9_]+$`)

func validateEnvironment(data []byte) error {
	if bytes.ContainsRune(data, 0) || bytes.Contains(data, []byte("REPLACE_WITH_")) {
		return errors.New("credential environment contains invalid data or placeholders")
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || !environmentName.MatchString(name) {
			return errors.New("credentials must use ALIBABA_CLOUD_* assignments, without export")
		}
		if strings.HasSuffix(value, "\\") {
			return errors.New("use single-line credential assignments")
		}
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			if len(value) < 2 || value[len(value)-1] != value[0] {
				return errors.New("unclosed quote in credential environment")
			}
		}
	}
	return nil
}

const (
	service    = "gitlab-runner-aliyun.service"
	account    = "gitlab-runner-aliyun"
	configDir  = "/etc/gitlab-runner-aliyun"
	homeDir    = "/var/lib/gitlab-runner-aliyun"
	receiptDir = "/var/lib/fleeting-plugin-aliyun"
	unitPath   = "/usr/lib/systemd/system/" + service
	binaryPath = "/usr/bin/fleeting-plugin-aliyun"
	runnerPath = "/usr/bin/gitlab-runner"
)

func table(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	return v
}

// Normalize a single Linux pool while preserving additional Runner settings.
// Parser errors may contain credentials; never return their source excerpts.
func configuration(input []byte, installed bool) ([]byte, error) {
	var doc map[string]any
	if _, err := toml.Decode(string(input), &doc); err != nil {
		return nil, errors.New("invalid Runner TOML; check the input file locally")
	}
	pools, ok := doc["runners"].([]map[string]any)
	if !ok || len(pools) != 1 {
		return nil, errors.New("the managed service requires exactly one [[runners]] entry")
	}
	pool := pools[0]
	if pool["executor"] != "instance" && pool["executor"] != "docker-autoscaler" {
		return nil, errors.New("executor must be instance or docker-autoscaler")
	}
	for _, key := range []string{"url", "token"} {
		value, _ := pool[key].(string)
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("Runner %s is required", key)
		}
	}
	address, _ := pool["url"].(string)
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil {
		return nil, errors.New("Runner URL must be an HTTP(S) URL without embedded credentials")
	}
	if pool["shell"] != "sh" || table(pool, "feature_flags")["FF_POSIXLY_CORRECT_ESCAPES"] != true {
		return nil, errors.New("use shell = sh and FF_POSIXLY_CORRECT_ESCAPES = true for this Linux service")
	}
	if n, _ := doc["concurrent"].(int64); n < 1 {
		return nil, errors.New("concurrent must be positive")
	}
	autoscaler := table(pool, "autoscaler")
	for _, key := range []string{"max_instances", "capacity_per_instance", "max_use_count"} {
		if n, _ := autoscaler[key].(int64); n < 1 {
			return nil, fmt.Errorf("autoscaler %s must be positive", key)
		}
	}
	if autoscaler["delete_instances_on_shutdown"] != true {
		return nil, errors.New("delete_instances_on_shutdown must be true for managed shutdown")
	}
	provider := table(autoscaler, "plugin_config")
	for _, key := range []string{"region", "scaling_group_id"} {
		if value, _ := provider[key].(string); strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("plugin_config %s is required", key)
		}
	}
	connector := table(autoscaler, "connector_config")
	if connector["protocol"] != "ssh" || connector["use_static_credentials"] != true {
		return nil, errors.New("the managed service requires SSH with use_static_credentials = true")
	}
	if name, _ := connector["username"].(string); strings.TrimSpace(name) == "" {
		return nil, errors.New("SSH username is required")
	}
	if pool["executor"] == "docker-autoscaler" {
		if image, _ := table(pool, "docker")["image"].(string); strings.TrimSpace(image) == "" {
			return nil, errors.New("Docker image is required")
		}
	}
	if installed && (autoscaler["plugin"] != binaryPath || connector["key_path"] != configDir+"/worker.key") {
		return nil, errors.New("managed plugin or SSH key path changed; restore the package-managed paths")
	}
	autoscaler["plugin"] = binaryPath
	connector["key_path"] = configDir + "/worker.key"
	var output bytes.Buffer
	if err := toml.NewEncoder(&output).Encode(doc); err != nil {
		return nil, errors.New("cannot encode Runner configuration")
	}
	if bytes.Contains(output.Bytes(), []byte("REPLACE_WITH_")) {
		return nil, errors.New("replace all REPLACE_WITH_ placeholders before setup")
	}
	return output.Bytes(), nil
}
