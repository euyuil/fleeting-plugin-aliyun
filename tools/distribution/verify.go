package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/hashicorp/go-hclog"
	fleeting "gitlab.com/gitlab-org/fleeting/fleeting"
	"gitlab.com/gitlab-org/fleeting/fleeting-artifact/pkg/installer"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

func verifyInstall(ctx context.Context, ref, version, revision, expected string) error {
	if ref == "" || version == "" || revision == "" || expected == "" {
		return errors.New("ref, version, revision, and expected-binary are required")
	}
	tag, err := name.NewTag(ref)
	if err != nil || tag.TagStr() != version {
		return errors.New("reference must use the exact expected version")
	}
	descriptor, err := remote.Get(tag, remote.WithContext(ctx), remote.WithAuth(authn.Anonymous))
	if err != nil {
		return fmt.Errorf("anonymous manifest download: %w", err)
	}
	path, err := installer.LookPath(ref, "")
	if err != nil {
		return err
	}
	installed, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	want, err := os.ReadFile(expected)
	if err != nil {
		return err
	}
	if !bytes.Equal(installed, want) {
		return errors.New("installed bytes differ from the release archive")
	}
	out, err := exec.CommandContext(ctx, path, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("installed executable version: %w", err)
	}
	fields := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok {
			fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if fields["Version"] != version || fields["Git revision"] != revision {
		return errors.New("installed version or source revision differs from the release")
	}
	process, err := fleeting.RunPlugin(path, []byte(`{}`), fleeting.WithPluginLogger(hclog.NewNullLogger()))
	if err != nil {
		return fmt.Errorf("plugin protocol loading: %w", err)
	}
	defer process.Kill()
	// Empty configuration must fail before credential construction or cloud calls.
	_, err = process.InstanceGroup().Init(ctx, hclog.NewNullLogger(), provider.Settings{})
	if err == nil || !strings.Contains(err.Error(), "region and scaling_group_id are required") {
		return errors.New("plugin did not reject empty configuration at the expected boundary")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"reference": ref, "manifest_digest": descriptor.Digest.String(),
		"version": version, "revision": revision, "os": runtime.GOOS, "architecture": runtime.GOARCH,
		"binary_sha256":      fmt.Sprintf("%x", sha256.Sum256(installed)),
		"anonymous_manifest": true, "release_bytes_match": true, "plugin_protocol": true,
		"empty_configuration_rejected": true,
	})
}
