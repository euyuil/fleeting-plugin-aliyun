package provider

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
	upstream "gitlab.com/gitlab-org/fleeting/fleeting/provider"
	"golang.org/x/crypto/ssh"
)

var posixUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func (g *InstanceGroup) temporaryKey(parent context.Context, info *upstream.ConnectInfo) error {
	if !posixUser.MatchString(info.Username) {
		return errors.New("dynamic SSH requires a POSIX username")
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	pub, err := ssh.NewPublicKey(public)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return err
	}
	expires := time.Now().UTC().Add(60 * time.Second)
	// The prepared helper accepts public material only and executes as the job
	// user. OpenSSH enforces expiry even if the Manager disappears.
	line := fmt.Sprintf("expiry-time=\"%s\",no-agent-forwarding,no-X11-forwarding,no-user-rc %s fleeting-expiry-%d", expires.Format("20060102150405Z"), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))), expires.Unix())
	encoded := base64.StdEncoding.EncodeToString([]byte(line))
	command := "/usr/local/bin/fleeting-authorize-key '" + encoded + "'"
	var response struct{ InvokeId, CommandId string }
	err = g.Client.API.Call(ctx, "ecs", "RunCommand", map[string]string{"RegionId": g.Region, "InstanceId.1": info.ID, "Type": "RunShellScript", "CommandContent": base64.StdEncoding.EncodeToString([]byte(command)), "ContentEncoding": "Base64", "Username": info.Username, "WorkingDir": "/", "Timeout": "20", "KeepCommand": "false", "EnableParameter": "false", "ClientToken": token()}, &response)
	if err != nil {
		return err
	}
	if response.InvokeId == "" {
		return errors.New("Cloud Assistant returned no invocation ID")
	}
	for {
		var out struct {
			Invocation struct {
				TotalCount        *int
				InvocationResults struct {
					InvocationResult []struct {
						InstanceId, InvokeId, InvokeRecordStatus, Output string
						ExitCode                                         *int
					}
				}
			}
		}
		err = g.Client.API.Call(ctx, "ecs", "DescribeInvocationResults", map[string]string{"RegionId": g.Region, "InvokeId": response.InvokeId, "InstanceId": info.ID, "ContentEncoding": "PlainText", "PageSize": "100"}, &out)
		if err != nil {
			return err
		}
		if out.Invocation.TotalCount == nil || *out.Invocation.TotalCount < 0 || *out.Invocation.TotalCount > 1 || *out.Invocation.TotalCount != len(out.Invocation.InvocationResults.InvocationResult) {
			return errors.New("incomplete Cloud Assistant invocation snapshot")
		}
		for _, r := range out.Invocation.InvocationResults.InvocationResult {
			if r.InstanceId != info.ID || r.InvokeId != response.InvokeId {
				return errors.New("Cloud Assistant invocation identity mismatch")
			}
			switch r.InvokeRecordStatus {
			case "Finished", "Success":
				if r.ExitCode == nil || *r.ExitCode != 0 || strings.TrimSpace(r.Output) != "fleeting-key-ready" {
					return errors.New("temporary SSH helper failed")
				}
				if time.Until(expires) < 20*time.Second {
					return errors.New("temporary SSH login window is too short; reacquire credentials")
				}
				info.Key = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
				info.Password = ""
				refresh := expires.Add(-10 * time.Second)
				info.Expires = &refresh
				return nil
			case "Failed", "Timeout", "Cancelled", "Stopped", "Terminated":
				return errors.New("Cloud Assistant could not authorize temporary SSH key")
			}
		}
		if err = aliyun.Wait(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
}
