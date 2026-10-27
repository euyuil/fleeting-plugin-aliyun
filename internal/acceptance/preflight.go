package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"slices"
	"strconv"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
	"golang.org/x/crypto/ssh"
)

func VerifyGitLabFixture(ctx context.Context, g gitlab, runnerID int64, tag, token, systemID string) error {
	var verified struct{ ID int64 }
	if err := g.requestAPI(ctx, "POST", "/runners/verify", map[string]string{"token": token, "system_id": systemID}, &verified); err != nil {
		return err
	}
	if verified.ID != runnerID {
		return errors.New("Runner token does not belong to the expected runner")
	}
	projectID, err := strconv.ParseInt(g.project, 10, 64)
	if err != nil || projectID <= 0 {
		return errors.New("fixture project ID must be a positive integer")
	}
	var available []struct{ ID int64 }
	if err := g.request(ctx, "GET", "/runners?per_page=100", nil, &available); err != nil {
		return err
	}
	if len(available) != 1 || available[0].ID != runnerID {
		return errors.New("fixture project must expose only its dedicated expected runner")
	}
	var runner struct {
		ID          int64
		RunnerType  string   `json:"runner_type"`
		RunUntagged bool     `json:"run_untagged"`
		TagList     []string `json:"tag_list"`
		Locked      bool
		Projects    []struct{ ID int64 }
		Paused      bool
	}
	if err := g.requestAPI(ctx, "GET", "/runners/"+strconv.FormatInt(runnerID, 10), nil, &runner); err != nil {
		return err
	}
	if runner.ID != runnerID || runner.RunnerType != "project_type" || runner.Paused || runner.RunUntagged || !runner.Locked || len(runner.Projects) != 1 || runner.Projects[0].ID != projectID || !slices.Contains(runner.TagList, tag) {
		return errors.New("expected runner must be an unpaused locked runner dedicated to the fixture project with untagged jobs disabled")
	}
	var active []job
	if err := g.request(ctx, "GET", "/jobs?scope[]=pending&scope[]=running&per_page=100", nil, &active); err != nil {
		return err
	}
	if len(active) > 0 {
		return errors.New("fixture has pending or running jobs; resolve prior occupancy before live acceptance")
	}
	return nil
}
func VerifyKeyPair(ctx context.Context, c *aliyun.Client, keyName string, private []byte) error {
	signer, err := ssh.ParsePrivateKey(private)
	if err != nil {
		return errors.New("worker private key is invalid or requires an unsupported interactive passphrase")
	}
	var out struct {
		TotalCount *int
		KeyPairs   struct {
			KeyPair []struct{ KeyPairName, PublicKey string }
		}
	}
	if err = c.API.Call(ctx, "ecs", "DescribeKeyPairs", map[string]string{"RegionId": c.Region, "KeyPairName": keyName, "IncludePublicKey": "true"}, &out); err != nil {
		return err
	}
	if out.TotalCount == nil || *out.TotalCount != 1 || len(out.KeyPairs.KeyPair) != 1 || out.KeyPairs.KeyPair[0].KeyPairName != keyName {
		return errors.New("launch-template SSH key pair was not found")
	}
	public, _, _, _, err := ssh.ParseAuthorizedKey([]byte(out.KeyPairs.KeyPair[0].PublicKey))
	if err != nil || !bytes.Equal(public.Marshal(), signer.PublicKey().Marshal()) {
		return errors.New("worker private key does not match the launch-template public key")
	}
	return nil
}

// VerifyRunnerBinary authenticates the executable before it can receive credentials.
func VerifyRunnerBinary(path, lockPath string) (string, error) {
	var lock struct {
		Runner struct {
			Version string
			SHA256  map[string]string
		}
	}
	raw, err := os.ReadFile(lockPath)
	if err != nil || json.Unmarshal(raw, &lock) != nil || lock.Runner.Version != RunnerVersion {
		return "", errors.New("Runner lock is unreadable or incompatible")
	}
	expected := lock.Runner.SHA256[runtime.GOOS+"-"+runtime.GOARCH]
	if len(expected) != 64 {
		return "", errors.New("Runner checksum is not pinned for this platform")
	}
	binary, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("Runner binary is unreadable")
	}
	digest := sha256.Sum256(binary)
	actual := hex.EncodeToString(digest[:])
	if actual != expected {
		return "", errors.New("Runner checksum differs from upstream.lock.json")
	}
	return actual, nil
}
