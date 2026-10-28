package component

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
	"github.com/euyuil/fleeting-plugin-aliyun/internal/testutil"
	"github.com/hashicorp/go-hclog"
	fleeting "gitlab.com/gitlab-org/fleeting/fleeting"
	"gitlab.com/gitlab-org/fleeting/fleeting/connector"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
	"golang.org/x/crypto/ssh"
)

func TestRealPluginProcessAndSSH(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	addr := startSSH(t, sshPub)
	host, portText, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portText)
	fake := testutil.New()
	fake.Add("i-local")
	i := fake.Instances["i-local"]
	i.PublicIpAddress.IpAddress = []string{host}
	fake.Instances["i-local"] = i
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call testutil.Call
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		var out json.RawMessage
		if err := fake.Call(r.Context(), call.Service, call.Action, call.Query, &out); err != nil {
			http.Error(w, "fake error", 500)
			return
		}
		_, _ = w.Write(out)
	}))
	defer server.Close()
	t.Setenv("ALIYUN_COMPONENT_ENDPOINT", server.URL)
	binary := filepath.Join(t.TempDir(), "fleeting-plugin-aliyun")
	cmd := exec.CommandContext(ctx, "go", "build", "-tags=componenttest", "-o", binary, "../../cmd/fleeting-plugin-aliyun")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	process, err := fleeting.RunPlugin(binary, []byte(`{"region":"cn-test-1","scaling_group_id":"asg-test","operation_timeout":"3s"}`), fleeting.WithPluginLogger(hclog.NewNullLogger()))
	if err != nil {
		t.Fatal(err)
	}
	defer process.Kill()
	group := process.InstanceGroup()
	settings := provider.Settings{ConnectorConfig: provider.ConnectorConfig{Username: "ci", UseStaticCredentials: true, Key: keyPEM, ProtocolPort: port, Timeout: 5 * time.Second}}
	info, err := group.Init(ctx, hclog.NewNullLogger(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "aliyun/cn-test-1/asg-test" || len(info.Capabilities) != 0 {
		t.Fatal(info)
	}
	var state provider.State
	if err = group.Update(ctx, func(id string, s provider.State) {
		if id == "i-local" {
			state = s
		}
	}); err != nil || state != provider.StateRunning {
		t.Fatal(state, err)
	}
	process.Kill()
	if err = group.Update(ctx, func(string, provider.State) {}); err == nil {
		t.Fatal("terminated plugin still served RPCs")
	}
	fake.Mu.Lock()
	beforeRestart := len(fake.Calls)
	fake.Mu.Unlock()
	restarted, err := fleeting.RunPlugin(binary, []byte(`{"region":"cn-test-1","scaling_group_id":"asg-test","operation_timeout":"3s"}`), fleeting.WithPluginLogger(hclog.NewNullLogger()))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Kill()
	group = restarted.InstanceGroup()
	if _, err = group.Init(ctx, hclog.NewNullLogger(), settings); err != nil {
		t.Fatal(err)
	}
	var rediscovered []string
	if err = group.Update(ctx, func(id string, s provider.State) {
		if s == provider.StateRunning {
			rediscovered = append(rediscovered, id)
		}
	}); err != nil || len(rediscovered) != 1 || rediscovered[0] != "i-local" {
		t.Fatal("new process did not rediscover the original worker", rediscovered, err)
	}
	fake.Mu.Lock()
	restartChangedCapacity := fake.Group.DesiredCapacity != 1 || len(fake.Instances) != 1
	for _, call := range fake.Calls[beforeRestart:] {
		if call.Action == "ScaleWithAdjustment" || call.Action == "RemoveInstances" {
			restartChangedCapacity = true
		}
	}
	fake.Mu.Unlock()
	if restartChangedCapacity {
		t.Fatal("restart mutated worker capacity")
	}
	if n, err := group.Increase(ctx, 1); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	connection, err := group.ConnectInfo(ctx, "i-local")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = connector.Run(ctx, connection, connector.ConnectorOptions{RunOptions: connector.RunOptions{Command: "printf component-ok", Stdout: &output}, DialOptions: connector.DialOptions{UseExternalAddr: true}})
	if err != nil || output.String() != "component-ok" {
		t.Fatal(output.String(), err)
	}
	if err = group.Heartbeat(ctx, "i-local"); err != nil {
		t.Fatal(err)
	}
	if _, err = group.Suspend(ctx, []string{"i-local"}); err == nil {
		t.Fatal("suspend unexpectedly supported")
	}
	if ids, err := group.Decrease(ctx, []string{"i-local"}); len(ids) != 1 || err != nil {
		t.Fatal(ids, err)
	}
	if _, err = group.Decrease(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err = group.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	// The pinned gRPC transport drops payloads alongside non-nil errors. Verify
	// that a failed second batch still returns the accepted first twenty IDs.
	fake.Mu.Lock()
	for n := 0; n < 21; n++ {
		fake.Add(fmt.Sprintf("i-partial-%02d", n))
	}
	removeCalls := 0
	fake.Before = func(action string, q map[string]string) error {
		if action == "RemoveInstances" {
			removeCalls++
			if removeCalls == 2 {
				return &aliyun.Error{Code: "Forbidden"}
			}
		}
		return nil
	}
	fake.Mu.Unlock()
	process2, err := fleeting.RunPlugin(binary, []byte(`{"region":"cn-test-1","scaling_group_id":"asg-test","operation_timeout":"3s"}`), fleeting.WithPluginLogger(hclog.NewNullLogger()))
	if err != nil {
		t.Fatal(err)
	}
	defer process2.Kill()
	group2 := process2.InstanceGroup()
	if _, err = group2.Init(ctx, hclog.NewNullLogger(), settings); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for n := 0; n < 21; n++ {
		ids = append(ids, fmt.Sprintf("i-partial-%02d", n))
	}
	accepted, err := group2.Decrease(ctx, ids)
	if err != nil || len(accepted) != 20 {
		t.Fatalf("partial accepted IDs lost across gRPC: %d %v", len(accepted), err)
	}
	fake.Mu.Lock()
	_, remaining := fake.Members["i-partial-20"]
	fake.Mu.Unlock()
	if !remaining {
		t.Fatal("failed second batch was removed")
	}

}
func startSSH(t *testing.T, expected ssh.PublicKey) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if c.User() != "ci" || !bytes.Equal(expected.Marshal(), k.Marshal()) {
			return nil, errors.New("unknown key")
		}
		return nil, nil
	}}
	cfg.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(reqs)
				for incoming := range chans {
					if incoming.ChannelType() != "session" {
						_ = incoming.Reject(ssh.UnknownChannelType, "session only")
						continue
					}
					ch, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					for req := range requests {
						if req.Type != "exec" {
							_ = req.Reply(false, nil)
							continue
						}
						var payload struct{ Command string }
						if ssh.Unmarshal(req.Payload, &payload) != nil {
							_ = req.Reply(false, nil)
							continue
						}
						_ = req.Reply(true, nil)
						if payload.Command == "printf component-ok" {
							_, _ = io.WriteString(ch, "component-ok")
							_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						} else {
							_, _ = io.WriteString(ch.Stderr(), fmt.Sprintf("unexpected command: %s", payload.Command))
							_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{1}))
						}
						_ = ch.Close()
						break
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { close(done); _ = listener.Close(); wg.Wait() })
	return listener.Addr().String()
}
