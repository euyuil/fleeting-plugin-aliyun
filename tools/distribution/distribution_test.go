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
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"gitlab.com/gitlab-org/fleeting/fleeting-artifact/pkg/installer"
)

func TestOfficialInstallerRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
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
	dockerConfig := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dockerConfig)
	t.Setenv("REGISTRY_AUTH_FILE", "")
	if err := os.WriteFile(filepath.Join(dockerConfig, "config.json"), []byte(`{"auths":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	original := remote.DefaultTransport
	target, _ := url.Parse(server.URL)
	remote.DefaultTransport = roundTripper(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme = target.Scheme
		r.URL.Host = target.Host
		return original.RoundTrip(r)
	})
	defer func() { remote.DefaultTransport = original }()
	ref := "registry.test/test/aliyun:0.1.0"
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
	version, err := exec.CommandContext(ctx, installed, "version").CombinedOutput()
	if err != nil || !strings.Contains(string(version), "0.1.0") {
		t.Fatal(string(version), err)
	}
	t.Setenv("FLEETING_PLUGIN_PATH", dest)
	if err := verifyInstall(ctx, ref, "0.1.0", revision, binary); err != nil {
		t.Fatal(err)
	}
	if err := verifyInstall(ctx, ref, "0.1.0", "unexpected-source", binary); err == nil {
		t.Fatal("wrong installed source accepted")
	}
	tampered := filepath.Join(t.TempDir(), "tampered")
	if err := os.WriteFile(tampered, []byte("different release bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyInstall(ctx, ref, "0.1.0", revision, tampered); err == nil {
		t.Fatal("different release bytes accepted")
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
