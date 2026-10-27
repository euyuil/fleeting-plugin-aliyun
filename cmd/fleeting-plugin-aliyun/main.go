package main

import (
	"github.com/euyuil/fleeting-plugin-aliyun/internal/provider"
	"gitlab.com/gitlab-org/fleeting/fleeting/plugin"
)

var version = "dev"
var revision = "unknown"
var builtAt = "unknown"

func main() {
	v := plugin.VersionInfo{Name: "fleeting-plugin-aliyun", Version: version, Revision: revision, BuiltAt: builtAt}
	plugin.Main(&provider.InstanceGroup{Version: version, BuildInfo: v.BuildInfo()}, v)
}
