package acceptance

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
	"golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRunnerChecksumBeforeExecution(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "runner")
	lock := filepath.Join(dir, "lock.json")
	data := []byte("synthetic executable")
	if err := os.WriteFile(binary, data, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	expected := hex.EncodeToString(digest[:])
	raw, _ := json.Marshal(map[string]any{"runner": map[string]any{"version": RunnerVersion, "sha256": map[string]string{runtime.GOOS + "-" + runtime.GOARCH: expected}}})
	if err := os.WriteFile(lock, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if actual, err := VerifyRunnerBinary(binary, lock); err != nil || actual != expected {
		t.Fatal(actual, err)
	}
	if err := os.WriteFile(binary, []byte("different executable with same advertised version"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRunnerBinary(binary, lock); err == nil {
		t.Fatal("modified executable accepted")
	}
}

type historyAPI struct {
	base          *acceptanceAPI
	group, status string
	ttlCalls      int
	failTTL       bool
}

func (a *historyAPI) Call(ctx context.Context, service, action string, q map[string]string, out any) error {
	if action == "DescribeScalingActivities" {
		raw, _ := json.Marshal(map[string]any{"TotalCount": 1, "ScalingActivities": map[string]any{"ScalingActivity": []map[string]any{{"ScalingActivityId": "asa-new", "ScalingGroupId": a.group, "StatusCode": a.status, "CreatedCapacity": 1, "CreatedInstances": map[string]any{"CreatedInstance": []string{"i-worker"}}}}}})
		return json.Unmarshal(raw, out)
	}
	if action == "ModifyInstanceAutoReleaseTime" {
		a.ttlCalls++
		if a.failTTL {
			return errors.New("temporary failure")
		}
	}
	return a.base.Call(ctx, service, action, q, out)
}
func TestActivityIdentityAndActiveState(t *testing.T) {
	c, base, l := fixture()
	api := &historyAPI{base: base, group: "asg-foreign", status: "Successful"}
	c.API = api
	if _, err := History(context.Background(), c); err == nil {
		t.Fatal("foreign history accepted")
	}
	api.group = c.GroupID
	api.status = "InProgress"
	if err := Claim(context.Background(), c, l); err == nil {
		t.Fatal("active pool claimed")
	}
	api.status = "Unknown"
	if _, err := History(context.Background(), c); err == nil {
		t.Fatal("unknown activity accepted")
	}
	if api.ttlCalls != 0 || base.mutations != 0 {
		t.Fatal("invalid history caused mutation")
	}
}
func TestAutoReleaseFailureRemainsRetryable(t *testing.T) {
	c, base, l := fixture()
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	base.fake.Add("i-worker")
	api := &historyAPI{base: base, group: c.GroupID, status: "Successful", failTTL: true}
	c.API = api
	if err := Observe(context.Background(), c, l, "ecs.test.small", "image-test", "cn-test-1a"); err == nil {
		t.Fatal("TTL failure hidden")
	}
	if l.Instances[0].AutoReleaseAt != "" {
		t.Fatal("unaccepted TTL recorded as accepted")
	}
	api.failTTL = false
	if err := Observe(context.Background(), c, l, "ecs.test.small", "image-test", "cn-test-1a"); err != nil {
		t.Fatal(err)
	}
	if api.ttlCalls != 2 || l.Instances[0].AutoReleaseAt == "" {
		t.Fatal("failed TTL was not retried")
	}
}

type keyPairAPI struct {
	public    string
	wrong     bool
	requested bool
}

func (a *keyPairAPI) Call(_ context.Context, _, action string, q map[string]string, out any) error {
	if action != "DescribeKeyPairs" || q["KeyPairName"] != "fixture-key" || q["KeyPairNames"] != "" {
		return errors.New("incorrect DescribeKeyPairs filter")
	}
	public := ""
	if q["IncludePublicKey"] == "true" {
		public = a.public
		a.requested = true
	}
	name := q["KeyPairName"]
	if a.wrong {
		name = "different-key"
	}
	raw, _ := json.Marshal(map[string]any{"TotalCount": 1, "KeyPairs": map[string]any{"KeyPair": []map[string]string{{"KeyPairName": name, "PublicKey": public}}}})
	return json.Unmarshal(raw, out)
}
func TestKeyPairQueryRequestsExactPublicKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	private := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	api := &keyPairAPI{public: string(ssh.MarshalAuthorizedKey(signer.PublicKey()))}
	c := &aliyun.Client{API: api, Region: "cn-test-1", GroupID: "asg-test"}
	if err := VerifyKeyPair(context.Background(), c, "fixture-key", private); err != nil || !api.requested {
		t.Fatal("exact public-key query failed", err)
	}
	api.wrong = true
	if err := VerifyKeyPair(context.Background(), c, "fixture-key", private); err == nil {
		t.Fatal("foreign key pair accepted")
	}
	api.wrong = false
	api.public = ""
	if err := VerifyKeyPair(context.Background(), c, "fixture-key", private); err == nil {
		t.Fatal("missing public key accepted")
	}
}
