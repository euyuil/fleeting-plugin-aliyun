package main

import (
	"fmt"
	"os"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/manager"
	"github.com/euyuil/fleeting-plugin-aliyun/internal/provider"
	"gitlab.com/gitlab-org/fleeting/fleeting/plugin"
)

var version = "dev"
var revision = "unknown"
var builtAt = "unknown"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "manager" {
		if err := manager.Run(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "manager:", err)
			os.Exit(1)
		}
		return
	}
	v := plugin.VersionInfo{Name: "fleeting-plugin-aliyun", Version: version, Revision: revision, BuiltAt: builtAt}
	plugin.Main(&provider.InstanceGroup{Version: version, BuildInfo: v.BuildInfo()}, v)
}
