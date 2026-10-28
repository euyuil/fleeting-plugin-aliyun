package acceptance

import (
	"bytes"
	"errors"
	"os"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"gitlab.com/gitlab-org/fleeting/fleeting-artifact/pkg/installer"
	"golang.org/x/mod/semver"
)

type smokeOptions struct {
	executor, reference, version, image string
}

func readSmokeOptions(get func(string) string) (smokeOptions, error) {
	o := smokeOptions{executor: get("ACCEPTANCE_EXECUTOR"), reference: get("PLUGIN_REFERENCE"), version: get("PLUGIN_VERSION"), image: get("ACCEPTANCE_JOB_IMAGE")}
	if o.executor == "" {
		o.executor = "instance"
	}
	if o.executor != "instance" && o.executor != "docker-autoscaler" {
		return o, errors.New("ACCEPTANCE_EXECUTOR must be instance or docker-autoscaler")
	}
	if o.executor == "docker-autoscaler" {
		if _, err := name.NewDigest(o.image, name.StrictValidation); err != nil || !strings.Contains(o.image, "@sha256:") {
			return o, errors.New("Docker acceptance requires ACCEPTANCE_JOB_IMAGE pinned by SHA-256 digest")
		}
	} else if o.image != "" {
		return o, errors.New("ACCEPTANCE_JOB_IMAGE is only valid for docker-autoscaler")
	}
	if o.reference == "" {
		o.reference = "fleeting-plugin-aliyun"
	}
	if o.reference != "fleeting-plugin-aliyun" {
		tag, err := name.NewTag(o.reference, name.StrictValidation)
		if err != nil || tag.Name() != o.reference || o.version == "" || semver.Canonical("v"+o.version) != "v"+o.version || strings.Contains(o.version, "+") || tag.TagStr() != o.version {
			return o, errors.New("OCI acceptance requires an exact PLUGIN_REFERENCE tag matching PLUGIN_VERSION")
		}
	}
	return o, nil
}

func verifyPluginVersion(output []byte, version, revision string) error {
	fields := make(map[string]string)
	for _, line := range strings.Split(string(output), "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok {
			fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if fields["Git revision"] != revision || (version != "" && fields["Version"] != version) {
		return errors.New("plugin version or source revision differs from requested release")
	}
	return nil
}

func verifyInstalledPlugin(reference string, expected []byte) error {
	path, err := installer.LookPath(reference, "")
	if err != nil {
		return errors.New("installed OCI plugin was not found")
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, expected) {
		return errors.New("installed OCI plugin differs from the verified release binary")
	}
	return nil
}

func verifyArtifactMode(a Artifact, executor string) error {
	if a.Executor != executor || a.ContainerDetected == nil || *a.ContainerDetected != (executor == "docker-autoscaler") {
		return errors.New("job artifact does not prove the requested VM or container execution mode")
	}
	return nil
}
