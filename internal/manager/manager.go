package manager

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh"
)

const help = `Usage: fleeting-plugin-aliyun manager <command>

  setup --config FILE --ssh-key FILE [--credentials FILE]
        Install a single Linux pool from a private Runner TOML file.
        Existing files are never overwritten. Repeating identical setup is safe.
  doctor    Check local configuration, permissions, systemd and Runner parsing.
  start     Check the setup, then enable and start the dedicated Runner service.
  stop      Stop accepting jobs and wait for running jobs to finish.
  status    Show the dedicated service status.

Requires Linux, systemd, root, and the Debian package. Setup does not start jobs.
Doctor makes no GitLab or cloud requests. It does not prove cloud permissions,
worker connectivity, job execution, or cleanup. Upgrade/remove with APT only
when stopped. Private configuration and state are retained on package removal.
`

type host struct {
	root       string
	output     io.Writer
	command    func(string, ...string) ([]byte, error)
	lookup     func() (int, int, error)
	chown      func(string, int, int) error
	checkOwner func(string, int, int) error
	fchown     func(*os.File, int, int) error
}

type receipt struct {
	Schema  int    `json:"schema"`
	Service string `json:"service"`
	UID     int    `json:"uid"`
	GID     int    `json:"gid"`
}

func Run(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(out, help)
		return err
	}
	if runtime.GOOS != "linux" {
		return errors.New("manager commands require Linux with systemd")
	}
	if os.Geteuid() != 0 {
		return errors.New("run manager commands as root (sudo)")
	}
	if args[0] != "status" {
		lock, err := os.OpenFile("/run/fleeting-plugin-aliyun-manager.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return errors.New("cannot open manager operation lock")
		}
		defer lock.Close()
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			return errors.New("another manager operation is in progress")
		}
	}
	if args[0] == "start" || args[0] == "setup" {
		if _, err := os.Lstat("/run/fleeting-plugin-aliyun-package-operation"); !errors.Is(err, os.ErrNotExist) {
			return errors.New("package operation is pending; finish or recover it with dpkg before setup/start")
		}
	}
	h := host{output: out, command: func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}, lookup: func() (int, int, error) {
		u, err := user.Lookup(account)
		if err != nil {
			return 0, 0, err
		}
		uid, err := strconv.Atoi(u.Uid)
		if err != nil {
			return 0, 0, err
		}
		gid, err := strconv.Atoi(u.Gid)
		if err != nil {
			return 0, 0, err
		}
		if uid == 0 || gid == 0 || u.HomeDir != homeDir {
			return 0, 0, errors.New("service account has incompatible identity or home")
		}
		group, err := user.LookupGroup(account)
		if err != nil || group.Gid != u.Gid {
			return 0, 0, errors.New("named service group does not match the account")
		}
		return uid, gid, nil
	}, chown: os.Chown, fchown: func(f *os.File, uid, gid int) error { return f.Chown(uid, gid) }}
	return h.run(args)
}

func (h host) path(path string) string { return filepath.Join(h.root, path) }

func (h host) run(args []string) error {
	switch args[0] {
	case "setup":
		flags := flag.NewFlagSet("setup", flag.ContinueOnError)
		flags.SetOutput(h.output)
		config := flags.String("config", "", "private Runner configuration")
		key := flags.String("ssh-key", "", "unencrypted worker SSH private key")
		credentials := flags.String("credentials", "", "systemd environment file for manager credentials (optional for instance roles)")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() != 0 || *config == "" || *key == "" {
			return errors.New("setup requires --config FILE and --ssh-key FILE")
		}
		return h.setup(*config, *key, *credentials)
	case "doctor", "start", "stop", "status":
		if len(args) != 1 {
			return errors.New("this command takes no arguments")
		}
	default:
		return errors.New("unknown manager command; use manager help")
	}
	if err := h.owned(); err != nil {
		return err
	}
	switch args[0] {
	case "doctor", "start":
		if err := h.doctor(); err != nil {
			return err
		}
		if args[0] == "start" {
			if _, err := h.command("systemctl", "enable", "--now", service); err != nil {
				return errors.New("service start failed; inspect its journal locally")
			}
			fmt.Fprintln(h.output, "Service started. Queued jobs can now create billable workers.")
		}
	case "stop":
		if _, err := h.command("systemctl", "stop", service); err != nil {
			return errors.New("service stop failed; retain configuration and inspect its journal locally")
		}
		if err := h.stopped(); err != nil {
			return err
		}
		fmt.Fprintln(h.output, "Service stopped. Confirm worker cleanup in cloud inventory before upgrading or removing the package. Configuration and recovery state were retained.")
	case "status":
		// Avoid journal excerpts: they can contain credentials or private identifiers.
		output, err := h.command("systemctl", "show", service, "--property=LoadState,ActiveState,SubState,UnitFileState")
		if err != nil {
			return errors.New("cannot read service status")
		}
		_, err = h.output.Write(output)
		return err
	}
	return nil
}

func readPrivate(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("cannot open private input file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1024*1024 {
		return nil, errors.New("input must be a regular private file (chmod 600), at most 1 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 {
		return nil, errors.New("cannot read bounded private file")
	}
	return data, nil
}

func (h host) unit() error {
	if err := regular(h.path(unitPath)); err != nil {
		return errors.New("install the Debian package first")
	}
	output, err := h.command("systemctl", "show", service, "--property=FragmentPath", "--value")
	fragment := strings.TrimSpace(string(output))
	if err != nil || (fragment != unitPath && fragment != "/lib/systemd/system/"+service) {
		return errors.New("service is unavailable or overridden; inspect systemd before setup")
	}
	output, err = h.command("systemctl", "show", service, "--property=DropInPaths", "--value")
	if err != nil || strings.TrimSpace(string(output)) != "" {
		return errors.New("service has overrides; this manager supports the packaged unit only")
	}
	return nil
}

func (h host) stopped() error {
	output, err := h.command("systemctl", "show", service, "--property=ActiveState", "--value")
	if err == nil && strings.TrimSpace(string(output)) == "failed" {
		return errors.New("service is failed; inspect its journal and confirm cloud cleanup, then run systemctl reset-failed gitlab-runner-aliyun.service")
	}
	if err != nil || strings.TrimSpace(string(output)) != "inactive" {
		return errors.New("service must be inactive; run manager stop first")
	}
	return nil
}

func (h host) setup(configFile, keyFile, environmentFile string) error {
	config, err := readPrivate(configFile)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	config, err = configuration(config, false)
	if err != nil {
		return err
	}
	key, err := readPrivate(keyFile)
	if err != nil {
		return fmt.Errorf("SSH key: %w", err)
	}
	if _, err := ssh.ParsePrivateKey(key); err != nil {
		return errors.New("worker key must be a valid unencrypted SSH private key")
	}
	environment := []byte("# Uses the service account's credential chain, for example an ECS RAM role.\n")
	if environmentFile != "" {
		environment, err = readPrivate(environmentFile)
		if err != nil {
			return fmt.Errorf("credentials: %w", err)
		}
	}
	if err := validateEnvironment(environment); err != nil {
		return err
	}
	if err := h.unit(); err != nil {
		return err
	}
	if err := h.stopped(); err != nil {
		return err
	}
	if err := regular(h.path(runnerPath)); err != nil {
		return errors.New("install GitLab Runner at /usr/bin/gitlab-runner first")
	}
	// The root-owned receipt reserves these paths before creating service-owned
	// files, so an interrupted setup can be retried without adopting other pools.
	marker := h.path(receiptDir + "/manager.json")
	if _, err := os.Lstat(marker); errors.Is(err, os.ErrNotExist) {
		for _, path := range []string{configDir, homeDir} {
			if _, err := os.Lstat(h.path(path)); !errors.Is(err, os.ErrNotExist) {
				return errors.New("reserved manager paths already exist without an installation receipt; preserve them and migrate manually")
			}
		}
		// An empty root-owned receipt directory can remain if creation was
		// interrupted before its first atomic marker write.
		if entries, err := os.ReadDir(h.path(receiptDir)); err == nil && len(entries) != 0 {
			return errors.New("unrecognized files in installation receipt directory")
		}
		if _, err := h.command("getent", "passwd", account); err == nil {
			return errors.New("service account already exists without an installation receipt; migrate manually")
		}
		if err := h.directory(receiptDir, 0, 0, 0700); err != nil {
			return err
		}
		// Persist intent before useradd. A retry may complete account creation.
		if err := h.write(marker, []byte("{\"schema\":1,\"service\":\""+service+"\",\"uid\":-1,\"gid\":-1}\n"), 0, 0); err != nil {
			return err
		}
	} else if err != nil {
		return errors.New("cannot inspect installation receipt")
	}
	r, err := h.loadReceipt()
	if err != nil {
		return err
	}
	if r.UID == -1 {
		if _, err := h.command("getent", "passwd", account); err != nil {
			if _, err := h.command("useradd", "--system", "--user-group", "--no-create-home", "--home-dir", homeDir, "--shell", "/usr/sbin/nologin", account); err != nil {
				return errors.New("cannot create dedicated service account; retry setup after resolving the local account error")
			}
		}
		r.UID, r.GID, err = h.lookup()
		if err != nil {
			return errors.New("cannot validate dedicated service account")
		}
		data, _ := json.Marshal(r)
		if err := h.write(marker, append(data, '\n'), 0, 0); err != nil {
			return err
		}
	}
	uid, gid, err := h.lookup()
	if err != nil || uid != r.UID || gid != r.GID {
		return errors.New("service account no longer matches installation receipt")
	}
	// Keep private configuration under a root-owned parent. Runner only needs
	// write access to its pre-created system ID file, not the directory itself.
	if err := h.directory(configDir, 0, gid, 0750); err != nil {
		return err
	}
	if err := h.directory(homeDir, uid, gid, 0700); err != nil {
		return err
	}
	// Compare every destination before creating any missing files. Preserve edits.
	files := []struct {
		name     string
		data     []byte
		uid, gid int
	}{
		{configDir + "/config.toml", config, uid, gid},
		{configDir + "/worker.key", key, uid, gid},
		{configDir + "/manager.env", environment, 0, 0},
	}
	for _, file := range files {
		if _, err := os.Lstat(h.path(file.name)); err == nil {
			existing, err := readPrivate(h.path(file.name))
			if err != nil || !bytes.Equal(existing, file.data) {
				return errors.New("existing private configuration differs; setup preserves it, edit it locally while stopped")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("cannot inspect private configuration")
		}
	}
	idPath := h.path(configDir + "/.runner_system_id")
	if _, err := os.Lstat(idPath); errors.Is(err, os.ErrNotExist) {
		var id [6]byte
		if _, err := rand.Read(id[:]); err != nil {
			return errors.New("cannot generate Runner system ID")
		}
		if err := h.write(idPath, []byte("s_"+hex.EncodeToString(id[:])+"\n"), uid, gid); err != nil {
			return err
		}
	}
	for _, file := range files {
		if _, err := os.Lstat(h.path(file.name)); errors.Is(err, os.ErrNotExist) {
			if err := h.write(h.path(file.name), file.data, file.uid, file.gid); err != nil {
				return err
			}
		}
	}
	if err := h.doctor(); err != nil {
		return err
	}
	fmt.Fprintln(h.output, "Setup complete. Service is stopped. Use manager start when ready to accept jobs.")
	return nil
}

func regular(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("expected a regular file")
	}
	return nil
}

func (h host) directory(path string, uid, gid int, mode os.FileMode) error {
	target := h.path(path)
	if info, err := os.Lstat(target); err == nil {
		if !info.IsDir() || info.Mode().Perm() != mode {
			return errors.New("managed directory has unsafe type or permissions")
		}
		return h.owner(target, uid, gid)
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect managed directory")
	}
	if err := os.Mkdir(target, mode); err != nil {
		return errors.New("cannot create managed directory")
	}
	return h.chown(target, uid, gid)
}

func (h host) write(path string, data []byte, uid, gid int) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".setup-*")
	if err != nil {
		return errors.New("cannot create private temporary file")
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return errors.New("cannot write private file")
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return errors.New("cannot sync private file")
	}
	if err := h.fchown(f, uid, gid); err != nil {
		f.Close()
		return errors.New("cannot set private file ownership")
	}
	if err := f.Close(); err != nil {
		return errors.New("cannot close private file")
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return errors.New("cannot install private file")
	}
	return nil
}

func (h host) owner(path string, uid, gid int) error {
	if h.checkOwner != nil {
		return h.checkOwner(path, uid, gid)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("cannot inspect managed path")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid || int(stat.Gid) != gid {
		return errors.New("managed path has unexpected ownership")
	}
	return nil
}

func (h host) loadReceipt() (receipt, error) {
	var r receipt
	info, err := os.Lstat(h.path(receiptDir))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return r, errors.New("installation receipt directory must have mode 0700")
	}
	if err := h.owner(h.path(receiptDir), 0, 0); err != nil {
		return r, err
	}
	marker := h.path(receiptDir + "/manager.json")
	if err := h.owner(marker, 0, 0); err != nil {
		return r, err
	}
	data, err := readPrivate(marker)
	if err != nil || json.Unmarshal(data, &r) != nil || r.Schema != 1 || r.Service != service {
		return r, errors.New("missing or invalid installation receipt; run manager setup")
	}
	return r, nil
}

func (h host) owned() error {
	r, err := h.loadReceipt()
	if err != nil {
		return err
	}
	uid, gid, err := h.lookup()
	if err != nil || uid != r.UID || gid != r.GID || uid <= 0 || gid <= 0 {
		return errors.New("service account no longer matches installation receipt")
	}
	return h.unit()
}

func (h host) doctor() error {
	if err := h.owned(); err != nil {
		return err
	}
	uid, gid, _ := h.lookup()
	for _, path := range []string{configDir, homeDir} {
		owner, mode := uid, os.FileMode(0700)
		if path == configDir {
			owner, mode = 0, 0750
		}
		info, err := os.Lstat(h.path(path))
		if err != nil || !info.IsDir() || info.Mode().Perm() != mode {
			return errors.New("managed directory has unsafe type or permissions")
		}
		if err := h.owner(h.path(path), owner, gid); err != nil {
			return err
		}
	}
	for _, file := range []string{"config.toml", "worker.key", "manager.env", ".runner_system_id"} {
		path := h.path(configDir + "/" + file)
		data, err := readPrivate(path)
		if err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		owner, group := uid, gid
		if file == "manager.env" {
			owner, group = 0, 0
		}
		if err := h.owner(path, owner, group); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		if file == "config.toml" {
			if _, err := configuration(data, true); err != nil {
				return err
			}
		}
		if file == "worker.key" {
			if _, err := ssh.ParsePrivateKey(data); err != nil {
				return errors.New("invalid worker SSH private key")
			}
		}
		if file == "manager.env" {
			if err := validateEnvironment(data); err != nil {
				return err
			}
		}
	}
	if err := regular(h.path(binaryPath)); err != nil {
		return errors.New("package plugin binary is missing")
	}
	if _, err := h.command("runuser", "-u", account, "--", binaryPath, "version"); err != nil {
		return errors.New("service account cannot execute the packaged plugin")
	}
	// Execute from HOME, even if sudo inherited an inaccessible working directory.
	// Discard Runner output because `list` can print authentication tokens.
	if _, err := h.command("runuser", "-u", account, "--", "env", "HOME="+homeDir, "/bin/sh", "-c", "cd \"$HOME\" && exec /usr/bin/gitlab-runner list --config /etc/gitlab-runner-aliyun/config.toml"); err != nil {
		return errors.New("Runner cannot parse the config as the service account; inspect local file permissions and Runner compatibility")
	}
	fmt.Fprintln(h.output, "PASS: local configuration, private files, packaged service and Runner parsing. GitLab, cloud permissions, worker connectivity and cleanup are not checked.")
	return nil
}
