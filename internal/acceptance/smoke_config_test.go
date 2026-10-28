package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSmokeModeAndReleaseInputs(t *testing.T) {
	digest := "docker.io/library/buildpack-deps@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name  string
		env   map[string]string
		valid bool
	}{
		{"native-default", map[string]string{}, true},
		{"oci-instance", map[string]string{"PLUGIN_REFERENCE": "ghcr.io/example/plugin:0.1.0-rc.2", "PLUGIN_VERSION": "0.1.0-rc.2"}, true},
		{"docker", map[string]string{"ACCEPTANCE_EXECUTOR": "docker-autoscaler", "ACCEPTANCE_JOB_IMAGE": digest}, true},
		{"unknown-executor", map[string]string{"ACCEPTANCE_EXECUTOR": "shell"}, false},
		{"floating-image", map[string]string{"ACCEPTANCE_EXECUTOR": "docker-autoscaler", "ACCEPTANCE_JOB_IMAGE": "docker.io/library/alpine:latest"}, false},
		{"missing-image", map[string]string{"ACCEPTANCE_EXECUTOR": "docker-autoscaler"}, false},
		{"unexpected-image", map[string]string{"ACCEPTANCE_JOB_IMAGE": digest}, false},
		{"floating-plugin", map[string]string{"PLUGIN_REFERENCE": "ghcr.io/example/plugin:latest", "PLUGIN_VERSION": "0.1.0-rc.2"}, false},
		{"different-plugin-version", map[string]string{"PLUGIN_REFERENCE": "ghcr.io/example/plugin:0.1.0-rc.1", "PLUGIN_VERSION": "0.1.0-rc.2"}, false},
		{"missing-plugin-version", map[string]string{"PLUGIN_REFERENCE": "ghcr.io/example/plugin:0.1.0-rc.2"}, false},
		{"minor-plugin-constraint", map[string]string{"PLUGIN_REFERENCE": "ghcr.io/example/plugin:0.1", "PLUGIN_VERSION": "0.1"}, false},
		{"major-plugin-constraint", map[string]string{"PLUGIN_REFERENCE": "ghcr.io/example/plugin:0", "PLUGIN_VERSION": "0"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readSmokeOptions(func(k string) string { return tc.env[k] })
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
}

func TestReleaseIdentityAndInstalledBytes(t *testing.T) {
	revision := strings.Repeat("a", 40)
	output := []byte("Version: 0.1.0-rc.2\nGit revision: " + revision + "\n")
	if err := verifyPluginVersion(output, "0.1.0-rc.2", revision); err != nil {
		t.Fatal(err)
	}
	for _, tc := range [][2]string{{"0.1.0-rc.1", revision}, {"0.1.0-rc.2", revision[:12]}} {
		if verifyPluginVersion(output, tc[0], tc[1]) == nil {
			t.Fatal("different version or revision prefix accepted")
		}
	}
	root := t.TempDir()
	t.Setenv("FLEETING_PLUGIN_PATH", root)
	dir := filepath.Join(root, "ghcr.io", "example", "plugin", "0.1.0-rc.2")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("verified release bytes")
	if err := os.WriteFile(filepath.Join(dir, "plugin"), data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := verifyInstalledPlugin("ghcr.io/example/plugin:0.1.0-rc.2", data); err != nil {
		t.Fatal(err)
	}
	if verifyInstalledPlugin("ghcr.io/example/plugin:0.1.0-rc.2", []byte("other build")) == nil {
		t.Fatal("different installed binary accepted")
	}
	if verifyInstalledPlugin("ghcr.io/example/plugin:0.1.0-rc.1", data) == nil {
		t.Fatal("different installed version accepted")
	}
}

func TestArtifactRequiresActualExecutionMode(t *testing.T) {
	for _, executor := range []string{"instance", "docker-autoscaler"} {
		container := executor == "docker-autoscaler"
		a := Artifact{Executor: executor, ContainerDetected: &container}
		if err := verifyArtifactMode(a, executor); err != nil {
			t.Fatal(err)
		}
		container = !container
		if verifyArtifactMode(a, executor) == nil {
			t.Fatal("different execution environment accepted")
		}
		a.ContainerDetected = nil
		if verifyArtifactMode(a, executor) == nil {
			t.Fatal("missing environment evidence accepted")
		}
	}
}
