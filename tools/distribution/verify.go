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
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/hashicorp/go-hclog"
	fleeting "gitlab.com/gitlab-org/fleeting/fleeting"
	"gitlab.com/gitlab-org/fleeting/fleeting-artifact/pkg/installer"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

func verifyInstall(ctx context.Context, ref, version, revision, expected, pinned, payload string) error {
	if ref == "" || version == "" || revision == "" || expected == "" {
		return errors.New("ref, version, revision, and expected-binary are required")
	}
	tag, err := name.NewTag(ref)
	if err != nil || tag.TagStr() != version {
		return errors.New("reference must use the exact expected version")
	}
	target := name.Reference(tag)
	if pinned != "" {
		immutable, err := name.NewDigest(pinned)
		if err != nil || immutable.Context().Name() != tag.Context().Name() {
			return errors.New("pinned OCI reference differs from the release repository")
		}
		target = immutable
	}
	if payload != "" && pinned == "" {
		return errors.New("candidate payload verification requires an immutable installation reference")
	}
	descriptor, err := remote.Get(target, remote.WithContext(ctx), remote.WithAuth(authn.Anonymous))
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
	var payloadHashes map[string]string
	platformDigest := ""
	if payload != "" {
		payloadHashes, err = verifyPayload(filepath.Dir(path), payload)
		if err != nil {
			return err
		}
		image, err := remote.Image(target, remote.WithContext(ctx), remote.WithAuth(authn.Anonymous),
			remote.WithPlatform(v1.Platform{OS: runtime.GOOS, Architecture: runtime.GOARCH}))
		if err != nil {
			return err
		}
		manifest, err := image.Manifest()
		if err != nil {
			return err
		}
		matching := 0
		for _, layer := range manifest.Layers {
			if value, exists := layer.Annotations["com.gitlab.fleeting.layer"]; exists {
				if value != version {
					return errors.New("OCI artifact layer version differs")
				}
				matching++
			}
		}
		if matching != 1 {
			return errors.New("exactly one versioned OCI artifact layer is required")
		}
		hash, err := image.Digest()
		if err != nil {
			return err
		}
		platformDigest = hash.String()
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"reference": ref, "manifest_digest": descriptor.Digest.String(), "pinned_reference": pinned,
		"candidate_sha": revision, "manifest_sha256": os.Getenv("MANIFEST_SHA256"),
		"run_id": os.Getenv("GITHUB_RUN_ID"), "run_attempt": os.Getenv("GITHUB_RUN_ATTEMPT"), "runner_image": os.Getenv("RUNNER_IMAGE"),
		"platform_manifest_digest": platformDigest, "payload_sha256": payloadHashes,
		"version": version, "revision": revision, "os": runtime.GOOS, "architecture": runtime.GOARCH,
		"binary_sha256":      fmt.Sprintf("%x", sha256.Sum256(installed)),
		"anonymous_manifest": true, "release_bytes_match": true, "plugin_protocol": true,
		"empty_configuration_rejected": true,
	})
}

// Resolve the version tag once, before invoking the official Runner installer.
// Verification subsequently fetches this digest, never a second mutable tag.
func pinReference(ctx context.Context, ref, version string) (string, error) {
	tag, err := name.NewTag(ref)
	if err != nil || tag.TagStr() != version || !validVersion(version) {
		return "", errors.New("an exact expected OCI version is required")
	}
	descriptor, err := remote.Get(tag, remote.WithContext(ctx), remote.WithAuth(authn.Anonymous))
	if err != nil {
		return "", err
	}
	return tag.Context().Digest(descriptor.Digest.String()).String(), nil
}

func verifyPayload(installed, expected string) (map[string]string, error) {
	names := []string{"plugin", "LICENSE", "NOTICE", "licenses.tar.gz"}
	for _, directory := range []string{installed, expected} {
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != len(names) {
			return nil, errors.New("OCI payload inventory is incomplete or contains extra files")
		}
	}
	hashes := map[string]string{}
	for _, file := range names {
		var contents [][]byte
		for _, directory := range []string{installed, expected} {
			path := filepath.Join(directory, file)
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				return nil, errors.New("OCI payload requires exact regular files")
			}
			value, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			contents = append(contents, value)
		}
		if !bytes.Equal(contents[0], contents[1]) {
			return nil, errors.New("installed OCI payload differs from the sealed candidate")
		}
		hashes[file] = fmt.Sprintf("%x", sha256.Sum256(contents[0]))
	}
	return hashes, nil
}
