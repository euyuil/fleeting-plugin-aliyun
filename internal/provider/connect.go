package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
	upstream "gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

func validateConnector(c upstream.ConnectorConfig) error {
	if c.Username == "" {
		return errors.New("connector username is required")
	}
	if c.OS == "windows" && ((c.Arch != "" && c.Arch != "amd64") || (c.Protocol != "" && c.Protocol != upstream.ProtocolWinRMHttps)) {
		return errors.New("Windows requires amd64 and WinRM HTTPS")
	}
	if c.OS == "linux" && c.Protocol != "" && c.Protocol != upstream.ProtocolSSH {
		return errors.New("Linux requires SSH")
	}
	if !upstream.IsValidPort(c.ProtocolPort) {
		return errors.New("invalid connector port")
	}
	if c.Protocol != "" {
		if err := c.Protocol.Valid(); err != nil {
			return err
		}
	}
	if c.OS != "" && c.OS != "linux" && c.OS != "windows" {
		return errors.New("only Linux and Windows workers are supported")
	}
	if c.Arch != "" && c.Arch != "amd64" && c.Arch != "arm64" {
		return errors.New("only amd64 and arm64 workers are supported")
	}
	if !c.UseStaticCredentials {
		return errors.New("use_static_credentials=true is required; dynamic credentials are not enabled")
	}
	return nil
}
func (g *InstanceGroup) ConnectInfo(parent context.Context, id string) (upstream.ConnectInfo, error) {
	ctx, done, err := g.readContext(parent)
	if err != nil {
		return upstream.ConnectInfo{}, err
	}
	defer done()
	members, err := g.Client.Members(ctx)
	if err != nil {
		return upstream.ConnectInfo{}, err
	}
	m, ok := members[id]
	if !ok {
		return upstream.ConnectInfo{}, upstream.ErrInstanceUnhealthy
	}
	if err = aliyun.ValidateMember(m); err != nil {
		return upstream.ConnectInfo{}, err
	}
	instances, err := g.Client.Instances(ctx, []string{id})
	if err != nil {
		return upstream.ConnectInfo{}, err
	}
	i, exists := instances[id]
	if !exists || i.Status != "Running" {
		return upstream.ConnectInfo{}, upstream.ErrInstanceUnhealthy
	}
	info := upstream.ConnectInfo{ID: id, ConnectorConfig: g.settings.ConnectorConfig}
	if info.OS == "" {
		info.OS = strings.ToLower(i.OSType)
	}
	if info.OS != "linux" && info.OS != "windows" {
		return info, errors.New("unknown worker OS; set connector os explicitly")
	}
	if info.Arch == "" {
		info.Arch, err = g.Client.Architecture(ctx, i.InstanceType)
		if err != nil {
			return info, err
		}
	}
	if info.Protocol == "" {
		info.Protocol = upstream.ProtocolSSH
		if info.OS == "windows" {
			info.Protocol = upstream.ProtocolWinRMHttps
		}
	}
	if info.OS == "windows" && (info.Protocol != upstream.ProtocolWinRMHttps || info.Arch != "amd64") {
		return info, errors.New("Windows requires amd64 and WinRM HTTPS")
	}
	if info.OS == "linux" && info.Protocol != upstream.ProtocolSSH {
		return info, errors.New("Linux requires SSH")
	}
	if info.Username == "" {
		return info, errors.New("connector username must be explicit")
	}
	if info.OS == "windows" && info.Password == "" {
		return info, errors.New("Windows requires a static connector password")
	}
	if info.ProtocolPort == 0 {
		info.ProtocolPort = upstream.DefaultProtocolPorts[info.Protocol]
	}
	if len(i.VpcAttributes.PrivateIpAddress.IpAddress) > 0 {
		info.InternalAddr = i.VpcAttributes.PrivateIpAddress.IpAddress[0]
	} else if len(i.InnerIpAddress.IpAddress) > 0 {
		info.InternalAddr = i.InnerIpAddress.IpAddress[0]
	}
	if i.EipAddress.IpAddress != "" {
		info.ExternalAddr = i.EipAddress.IpAddress
	} else if len(i.PublicIpAddress.IpAddress) > 0 {
		info.ExternalAddr = i.PublicIpAddress.IpAddress[0]
	}
	if info.ExternalAddr == "" && info.InternalAddr == "" {
		return info, fmt.Errorf("instance %s has no usable IP address yet", id)
	}

	if g.SSHCredentials == "cloud_assistant" {
		if info.OS != "linux" || info.Protocol != upstream.ProtocolSSH {
			return info, errors.New("dynamic credentials require Linux SSH")
		}
		if err = g.temporaryKey(ctx, &info); err != nil {
			return upstream.ConnectInfo{}, err
		}
	}
	return info, nil
}
