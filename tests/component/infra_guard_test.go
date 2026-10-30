package component

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInfrastructureSetupPreservesExistingReceipts(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".live"), 0700); err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(root, ".live", "vpc.json")
	original := []byte(`{"VpcId":"vpc-existing"}`)
	if err := os.WriteFile(receipt, original, 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	mock := []byte("#!/bin/sh\nif test \"$1\" = version; then echo 3.5.2; exit 0; fi\ntouch \"$TEST_UNEXPECTED_MUTATION\"\nexit 99\n")
	if err := os.WriteFile(filepath.Join(bin, "aliyun"), mock, 0700); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("../../infra/prepare.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", script)
	cmd.Dir = root
	marker := filepath.Join(root, "unexpected")
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "CREATE_FIXTURE=1", "ALIYUN_REGION=cn-test-1", "ZONE_ID=cn-test-1a", "INSTANCE_TYPE=ecs.test.small", "IMAGE_ID=image-test", "SSH_PUBLIC_KEY_FILE=key.pub", "MANAGER_CIDR=192.0.2.1/32", "ACCEPTANCE_PROJECT=owner/repo", "TEST_UNEXPECTED_MUTATION="+marker)
	if out, e := cmd.CombinedOutput(); e == nil {
		t.Fatal("existing setup unexpectedly accepted", string(out))
	}
	if _, e := os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("create API called before receipt guard")
	}
	got, e := os.ReadFile(receipt)
	if e != nil || string(got) != string(original) {
		t.Fatal("existing receipt overwritten")
	}
}

func TestBootstrapRejectsPrivateKeyBeforePackageChanges(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "unexpected")
	if err := os.WriteFile(filepath.Join(root, "apt-get"), []byte("#!/bin/sh\ntouch \"$TEST_UNEXPECTED_MUTATION\"\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("../../infra/bootstrap-linux.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", script)
	cmd.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "TEST_UNEXPECTED_MUTATION="+marker, "RUNNER_VERSION=19.5.1", "RUNNER_SHA256=synthetic", "WORKER_PUBLIC_KEY=-----BEGIN PRIVATE KEY-----", "GIT_PACKAGE_VERSION=synthetic", "CURL_PACKAGE_VERSION=synthetic", "CA_PACKAGE_VERSION=synthetic", "SSH_PACKAGE_VERSION=synthetic", "COREUTILS_PACKAGE_VERSION=synthetic")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatal("private key admitted", string(out))
	} else if !strings.Contains(string(out), "Provide one OpenSSH public key, never a private key.") {
		t.Fatal("bootstrap did not reach the intended private-key rejection", string(out))
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("package change preceded public-key validation")
	}
}
