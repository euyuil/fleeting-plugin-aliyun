package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"gitlab.com/gitlab-org/fleeting/fleeting-artifact/pkg/installer"
)

func TestOfficialInstallerRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	packages := t.TempDir()
	folder := filepath.Join(packages, runtime.GOOS, runtime.GOARCH)
	if err := os.MkdirAll(folder, 0755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(folder, "plugin")
	const revision = "0000000000000000000000000000000000000001"
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-X main.version=0.1.0 -X main.revision="+revision, "-o", binary, "../../cmd/fleeting-plugin-aliyun")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	notices := t.TempDir()
	for path, content := range map[string]string{
		"LICENSE": "project license", "LICENSES/retained.txt": "retained notice",
		"_licenses/dependency/LICENSE": "dependency license", "_licenses/Go/PATENTS": "Go patents",
		"dependencies.json": `[{"licenses":["_licenses/dependency/LICENSE"]}]`,
	} {
		path = filepath.Join(notices, path)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	python := os.Getenv("PYTHON")
	if python == "" {
		python = "python3"
	}
	// Use the production staging function, including nested license material.
	cmd = exec.CommandContext(ctx, python, "-c", `
import pathlib, sys
sys.path.insert(0, "..")
from package_layout import stage_oci_notices
root = pathlib.Path(sys.argv[2])
stage_oci_notices(pathlib.Path(sys.argv[1]), [(n, root / n) for n in ("LICENSE", "LICENSES", "_licenses", "dependencies.json")], 1234567890)
`, folder, notices)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	checkInstallerRoundTrip(t, ctx, packages, binary, "0.1.0", revision)
}

func TestBuiltDistributionRoundTrip(t *testing.T) {
	packages := os.Getenv("OCI_PACKAGES")
	if packages == "" {
		t.Skip("make dist supplies the complete generated package directory")
	}
	if runtime.GOOS != "linux" {
		t.Skip("built distribution execution requires a native Linux host")
	}
	version, revision := os.Getenv("VERSION"), os.Getenv("REVISION")
	if version == "" || revision == "" {
		t.Fatal("VERSION and REVISION must identify the built distribution")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	checkInstallerRoundTrip(t, ctx, packages, filepath.Join(packages, runtime.GOOS, runtime.GOARCH, "plugin"), version, revision)
}

func checkInstallerRoundTrip(t *testing.T, ctx context.Context, packages, binary, version, revision string) {
	t.Helper()
	folder := filepath.Dir(binary)
	files, err := os.ReadDir(folder)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if !file.Type().IsRegular() {
			t.Fatal("OCI platform payload must contain root-level regular files only", file.Name())
		}
	}
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	dockerConfig := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dockerConfig)
	t.Setenv("REGISTRY_AUTH_FILE", "")
	if err := os.WriteFile(filepath.Join(dockerConfig, "config.json"), []byte(`{"auths":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	original := remote.DefaultTransport
	target, _ := url.Parse(server.URL)
	var versionTagReads atomic.Int64
	remote.DefaultTransport = roundTripper(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/manifests/"+version) {
			versionTagReads.Add(1)
		}
		r = r.Clone(r.Context())
		r.URL.Scheme = target.Scheme
		r.URL.Host = target.Host
		return original.RoundTrip(r)
	})
	defer func() { remote.DefaultTransport = original }()
	ref := "registry.localhost/test/aliyun:" + version
	if err := publish(ctx, ref, packages); err != nil {
		t.Fatal(err)
	}
	if err := publish(ctx, ref, packages); err == nil {
		t.Fatal("immutable version overwritten")
	}
	dest := t.TempDir()
	if err := installer.Install(ctx, ref, installer.WithDirectory(dest)); err != nil {
		t.Fatal(err)
	}
	installed, err := installer.LookPath(ref, dest)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("installed bytes differ")
	}
	versionOutput, err := exec.CommandContext(ctx, installed, "version").CombinedOutput()
	if err != nil || !strings.Contains(string(versionOutput), version) {
		t.Fatal(string(versionOutput), err)
	}
	for _, file := range []string{"LICENSE", "NOTICE", "licenses.tar.gz"} {
		want, err := os.ReadFile(filepath.Join(folder, file))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(filepath.Dir(installed), file))
		if err != nil || string(got) != string(want) {
			t.Fatal("installed license material differs", file, err)
		}
	}
	t.Setenv("FLEETING_PLUGIN_PATH", dest)
	if err := verifyInstall(ctx, ref, version, revision, binary, "", ""); err != nil {
		t.Fatal(err)
	}
	pinned, err := pinReference(ctx, ref, version)
	if err != nil || !strings.Contains(pinned, "@sha256:") {
		t.Fatal("exact release was not pinned", pinned, err)
	}
	pinnedDirectory := t.TempDir()
	if err := installer.Install(ctx, pinned, installer.WithDirectory(pinnedDirectory)); err != nil {
		t.Fatal("official installer rejected the immutable reference", err)
	}
	t.Setenv("FLEETING_PLUGIN_PATH", pinnedDirectory)
	versionTagReads.Store(0)
	if err := verifyInstall(ctx, ref, version, revision, binary, pinned, folder); err != nil {
		t.Fatal("immutable full payload verification failed", err)
	}
	if versionTagReads.Load() != 0 {
		t.Fatal("verification fetched the mutable tag after installation")
	}
	if runner := os.Getenv("RUNNER_BINARY"); runner != "" {
		cliDirectory := t.TempDir()
		config := filepath.Join(t.TempDir(), "runner.toml")
		writeConfig := func(reference string) {
			text := "[[runners]]\n executor = \"instance\"\n [runners.autoscaler]\n plugin = \"" + reference + "\"\n"
			if err := os.WriteFile(config, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
		}
		invoke := func(args ...string) {
			command := exec.CommandContext(ctx, runner, append([]string{"fleeting", "--config", config}, args...)...)
			// .localhost selects plain HTTP in the pinned registry client. The
			// child uses this disposable registry as its proxy, without DNS,
			// host-file changes, trust-store changes or a privileged port.
			command.Env = append(os.Environ(), "FLEETING_PLUGIN_PATH="+cliDirectory, "DOCKER_CONFIG="+dockerConfig,
				"DOCKER_AUTH_CONFIG=", "REGISTRY_AUTH_FILE=", "HTTP_PROXY="+server.URL, "http_proxy="+server.URL,
				"HTTPS_PROXY="+server.URL, "https_proxy="+server.URL, "NO_PROXY=", "no_proxy=")
			if out, err := command.CombinedOutput(); err != nil {
				t.Fatalf("official Runner CLI installation: %v\n%s", err, out)
			}
		}
		writeConfig(pinned)
		invoke("install", "--upgrade")
		writeConfig(ref) // Only the local list lookup uses a semantic version.
		invoke("list")
		t.Setenv("FLEETING_PLUGIN_PATH", cliDirectory)
		if err := verifyInstall(ctx, ref, version, revision, binary, pinned, folder); err != nil {
			t.Fatal("official Runner CLI installed a different payload", err)
		}
	}
	if err := verifyInstall(ctx, ref, version, revision, binary, "", folder); err == nil {
		t.Fatal("candidate payload was accepted without an immutable reference")
	}
	if err := verifyInstall(ctx, ref, version, "unexpected-source", binary, "", ""); err == nil {
		t.Fatal("wrong installed source accepted")
	}
	tampered := filepath.Join(t.TempDir(), "tampered")
	if err := os.WriteFile(tampered, []byte("different release bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyInstall(ctx, ref, version, revision, tampered, "", ""); err == nil {
		t.Fatal("different release bytes accepted")
	}
}

func TestPayloadVerificationRejectsChangedLicenseAndIncompleteInventory(t *testing.T) {
	for _, fault := range []string{"license", "notice", "missing", "extra", "symlink"} {
		t.Run(fault, func(t *testing.T) {
			installed, expected := t.TempDir(), t.TempDir()
			for _, dir := range []string{installed, expected} {
				for _, file := range []string{"plugin", "LICENSE", "NOTICE", "licenses.tar.gz"} {
					if err := os.WriteFile(filepath.Join(dir, file), []byte(file), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := verifyPayload(installed, expected); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "license":
				_ = os.WriteFile(filepath.Join(installed, "LICENSE"), []byte("changed"), 0600)
			case "notice":
				_ = os.WriteFile(filepath.Join(installed, "NOTICE"), []byte("changed"), 0600)
			case "missing":
				_ = os.Remove(filepath.Join(installed, "licenses.tar.gz"))
			case "extra":
				_ = os.WriteFile(filepath.Join(installed, "undeclared"), []byte("extra"), 0600)
			case "symlink":
				_ = os.Remove(filepath.Join(installed, "NOTICE"))
				if err := os.Symlink(filepath.Join(expected, "NOTICE"), filepath.Join(installed, "NOTICE")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := verifyPayload(installed, expected); err == nil {
				t.Fatal("modified or incomplete OCI payload accepted")
			}
		})
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestExactSemanticVersions(t *testing.T) {
	for _, version := range []string{"0.1.0", "1.2.3-rc.1"} {
		if !validVersion(version) {
			t.Fatal("valid version rejected", version)
		}
	}
	for _, version := range []string{"latest", "stable", "1", "1.2", "v1.2.3", "01.2.3", "1.2.3-01", "1.2.3+build"} {
		if validVersion(version) {
			t.Fatal("noncanonical version accepted", version)
		}
	}
}
