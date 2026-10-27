package component

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestTemporaryKeyHelperFileBehavior(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	// The shims redirect passwd lookup to a temporary account and use the OS
	// flock system call; the production helper and file operations stay real.
	if err = os.WriteFile(filepath.Join(bin, "getent"), []byte("#!/bin/sh\nprintf '%s:x:1000:1000:Test:%s:/bin/sh\\n' \"$(id -un)\" \"$TEST_HOME\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bin, "flock"), []byte("#!"+python+"\nimport fcntl,sys\nfcntl.flock(int(sys.argv[-1]),fcntl.LOCK_EX)\n"), 0700); err != nil {
		t.Fatal(err)
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	public := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	line := func(expiry time.Time) string {
		return fmt.Sprintf("expiry-time=\"%s\",no-agent-forwarding,no-X11-forwarding,no-user-rc %s fleeting-expiry-%d", expiry.UTC().Format("20060102150405Z"), public, expiry.Unix())
	}
	invoke := func(text string) error {
		cmd := exec.Command("sh", "../../infra/fleeting-authorize-key", base64.StdEncoding.EncodeToString([]byte(text)))
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "TEST_HOME="+home, "HOME=/nonexistent/incorrect")
		out, e := cmd.CombinedOutput()
		if e != nil {
			return fmt.Errorf("helper: %v %s", e, out)
		}
		return nil
	}
	expired := line(time.Now().Add(-time.Minute))
	future := line(time.Now().Add(2 * time.Minute))
	if err = invoke(expired); err != nil {
		t.Fatal(err)
	}
	if err = invoke(future); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, ".ssh", "fleeting-keys")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), expired) || !strings.Contains(string(b), future) {
		t.Fatal("expiry pruning failed")
	}
	if err = invoke("invalid public material"); err == nil {
		t.Fatal("invalid key line accepted")
	}
	if err = invoke(future + "\n" + public); err == nil {
		t.Fatal("multiline key material accepted")
	}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			if e := invoke(line(time.Now().Add(time.Duration(180+offset) * time.Second))); e != nil {
				t.Error(e)
			}
		}(n)
	}
	wg.Wait()
	b, err = os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Split(strings.TrimSpace(string(b)), "\n")) != 9 {
		t.Fatal("concurrent registration lost a key")
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("key file permissions", err)
	}
}
