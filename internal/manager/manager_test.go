package manager

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func sample(t *testing.T, mode string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../examples/" + mode + "/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.NewReplacer("REPLACE_WITH_PROJECT_RUNNER_AUTH_TOKEN", "synthetic-token-do-not-log", "REPLACE_WITH_DEDICATED_GROUP_ID", "synthetic-group", "REPLACE_WITH_PINNED_JOB_IMAGE_DIGEST", "alpine:3.21").Replace(string(b))
	return []byte(s)
}

func TestConfiguration(t *testing.T) {
	for _, mode := range []string{"instance", "docker-autoscaler"} {
		t.Run(mode, func(t *testing.T) {
			b, err := configuration(append(sample(t, mode), []byte("\n# Comment\n")...), false)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(b, []byte(binaryPath)) || bytes.Contains(b, []byte("ghcr.io")) {
				t.Fatal("expected native plugin")
			}
			if _, err := configuration(b, true); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, pair := range [][2]string{{"delete_instances_on_shutdown = true", "delete_instances_on_shutdown = false"}, {"max_instances = 1", "max_instances = 0"}, {"executor = \"instance\"", "executor = \"shell\""}, {"synthetic-group", "REPLACE_WITH_GROUP"}, {"shell = \"sh\"", "shell = \"bash\""}} {
		input := strings.Replace(string(sample(t, "instance")), pair[0], pair[1], 1)
		if _, err := configuration([]byte(input), false); err == nil {
			t.Fatalf("accepted invalid setting %q", pair[0])
		}
	}
	_, err := configuration([]byte("token = \"secret-do-not-print"), false)
	if err == nil || strings.Contains(err.Error(), "secret-do-not-print") {
		t.Fatal("parser must redact sensitive excerpts")
	}
}

type fixture struct {
	host
	output        bytes.Buffer
	calls         []string
	state         string
	accountExists bool
	runnerFailure bool
	config, key   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{state: "inactive"}
	f.root = t.TempDir()
	f.host.output = &f.output
	owners := map[string][2]int{}
	f.chown = func(path string, uid, gid int) error { owners[path] = [2]int{uid, gid}; return nil }
	// Temp files are atomically renamed; ownership itself is covered by the real
	// Debian lifecycle test. Unit tests exercise the permission and identity paths.
	f.checkOwner = func(string, int, int) error { return nil }
	f.fchown = func(*os.File, int, int) error { return nil }
	f.lookup = func() (int, int, error) {
		if !f.accountExists {
			return 0, 0, errors.New("missing")
		}
		return 1001, 1001, nil
	}
	f.command = func(name string, args ...string) ([]byte, error) {
		call := name + " " + strings.Join(args, " ")
		f.calls = append(f.calls, call)
		switch name {
		case "getent":
			if !f.accountExists {
				return nil, errors.New("missing")
			}
		case "useradd":
			f.accountExists = true
		case "runuser":
			if f.runnerFailure {
				return []byte("private token value"), errors.New("invalid")
			}
		case "systemctl":
			if strings.Contains(call, "FragmentPath") {
				return []byte(unitPath + "\n"), nil
			}
			if strings.Contains(call, "ActiveState") {
				return []byte(f.state + "\n"), nil
			}
			if args[0] == "stop" {
				f.state = "inactive"
			}
			if args[0] == "enable" {
				f.state = "active"
			}
		}
		return nil, nil
	}
	for _, path := range []string{"/etc", "/var/lib", filepath.Dir(unitPath), filepath.Dir(runnerPath)} {
		if err := os.MkdirAll(f.path(path), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{unitPath, runnerPath, binaryPath} {
		if err := os.WriteFile(f.path(path), []byte("fixture"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	f.config = filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(f.config, sample(t, "instance"), 0600); err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	f.key = filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(f.key, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestSetupRepeatPreservesConfiguration(t *testing.T) {
	f := newFixture(t)
	if err := f.setup(f.config, f.key, ""); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(f.path(configDir + "/config.toml"))
	if err := f.setup(f.config, f.key, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(f.calls, "\n"), "enable") {
		t.Fatal("setup started service")
	}
	edited := append(first, []byte("\n# User edit\n")...)
	os.WriteFile(f.path(configDir+"/config.toml"), edited, 0600)
	if err := f.setup(f.config, f.key, ""); err == nil {
		t.Fatal("overwrote existing user configuration")
	}
	after, _ := os.ReadFile(f.path(configDir + "/config.toml"))
	if !bytes.Equal(edited, after) {
		t.Fatal("lost user edits")
	}
	if strings.Contains(f.output.String(), "synthetic-token") {
		t.Fatal("printed secret")
	}
}

func TestSetupFailuresPreserveOtherInstallations(t *testing.T) {
	for _, kind := range []string{"existing-directory", "symlink", "existing-account", "active", "private-mode", "invalid-key"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			switch kind {
			case "existing-directory":
				os.Mkdir(f.path(configDir), 0700)
			case "symlink":
				os.Symlink(t.TempDir(), f.path(configDir))
			case "existing-account":
				f.accountExists = true
			case "active":
				f.state = "active"
			case "private-mode":
				os.Chmod(f.config, 0644)
			case "invalid-key":
				os.WriteFile(f.key, []byte("private-invalid-key"), 0600)
			}
			if err := f.setup(f.config, f.key, ""); err == nil {
				t.Fatal("unsafe setup accepted")
			}
			if _, err := os.Stat(f.path(configDir + "/config.toml")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("wrote configuration on failed preflight")
			}
		})
	}
}

func TestRecoveryAndDiagnostics(t *testing.T) {
	f := newFixture(t)
	f.runnerFailure = true
	if err := f.setup(f.config, f.key, ""); err == nil || strings.Contains(err.Error(), "private token") {
		t.Fatal("must reject and redact Runner failure")
	}
	f.runnerFailure = false
	if err := f.setup(f.config, f.key, ""); err != nil {
		t.Fatal("setup retry:", err)
	}
	if err := f.run([]string{"start"}); err != nil {
		t.Fatal(err)
	}
	if f.state != "active" {
		t.Fatal("not started")
	}
	if err := f.setup(f.config, f.key, ""); err == nil {
		t.Fatal("setup during job execution")
	}
	if err := f.run([]string{"stop"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.path(configDir + "/worker.key")); err != nil {
		t.Fatal("stop removed key")
	}
	os.Chmod(f.path(configDir+"/config.toml"), 0644)
	if err := f.run([]string{"start"}); err == nil {
		t.Fatal("started with exposed config")
	}
	if f.state != "inactive" {
		t.Fatal("failed preflight started service")
	}
}

func TestHelpIsAvailableWithoutRoot(t *testing.T) {
	var output bytes.Buffer
	if err := Run([]string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "doctor") {
		t.Fatal("missing usage")
	}
}

func TestEnvironmentRejectsUnexpandedCredentials(t *testing.T) {
	for _, value := range []string{"export ALIBABA_CLOUD_ACCESS_KEY_ID=secret", "ALIBABA_CLOUD_ACCESS_KEY_ID=REPLACE_WITH_ID", "HOME=/root", "ALIBABA_CLOUD_ACCESS_KEY_ID=\"secret", "ALIBABA_CLOUD_ACCESS_KEY_ID=secret\\"} {
		err := validateEnvironment([]byte(value))
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe or sensitive environment error")
		}
	}
	if err := validateEnvironment([]byte("# Role\nALIBABA_CLOUD_ECS_METADATA=ci-role\n")); err != nil {
		t.Fatal(err)
	}
}

func TestRetryAfterReceiptDirectoryCreation(t *testing.T) {
	f := newFixture(t)
	if err := os.Mkdir(f.path(receiptDir), 0700); err != nil {
		t.Fatal(err)
	}
	if err := f.setup(f.config, f.key, ""); err != nil {
		t.Fatal(err)
	}
	os.Chmod(f.path(receiptDir), 0777)
	if err := f.run([]string{"doctor"}); err == nil {
		t.Fatal("accepted unsafe receipt directory")
	}
}

func TestReadPrivateRejectsSymlinks(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(t.TempDir(), "link")
	os.Symlink(f.key, path)
	if _, err := readPrivate(path); err == nil {
		t.Fatal("followed private input symlink")
	}
}
