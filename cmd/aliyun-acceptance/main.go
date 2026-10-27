//go:build live

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/acceptance"
	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

func main() { os.Exit(run()) }
func run() int {
	mode := "smoke"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	dir := os.Getenv("ACCEPTANCE_OUTPUT")
	if dir == "" {
		dir = ".live"
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if mode == "smoke" {
		r := acceptance.Run(ctx, dir)
		fmt.Println(r.Result)
		if r.Result != "PASS" {
			return 1
		}
		return 0
	}
	if mode != "cleanup" && mode != "janitor" {
		fmt.Fprintln(os.Stderr, "supported modes: smoke, cleanup, janitor")
		return 2
	}
	if os.Getenv("RUN_LIVE_ACCEPTANCE") != "1" {
		fmt.Fprintln(os.Stderr, "RUN_LIVE_ACCEPTANCE=1 is required")
		return 2
	}
	region, group, project := os.Getenv("ALIYUN_REGION"), os.Getenv("ALIYUN_SCALING_GROUP_ID"), os.Getenv("ACCEPTANCE_PROJECT")
	if region == "" || group == "" || project == "" {
		fmt.Fprintln(os.Stderr, "exact region, group and project are required")
		return 2
	}
	ctx, timeout := context.WithTimeout(ctx, 8*time.Minute)
	defer timeout()
	sdk, err := aliyun.NewSDK(region)
	if sdk != nil {
		defer sdk.Close()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	c := &aliyun.Client{API: sdk, Region: region, GroupID: group}
	g, err := c.Group(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	parts := strings.Split(g.Tag(acceptance.RunTag), "/")
	if len(parts) != 2 {
		if g.Tag(acceptance.RunTag) == "" && g.DesiredCapacity == 0 && g.TotalCapacity == 0 {
			fmt.Println("idle pool; no run to clean")
			return 0
		}
		fmt.Fprintln(os.Stderr, "invalid run marker")
		return 1
	}
	if mode == "cleanup" && (parts[0] != os.Getenv("GITHUB_RUN_ID") || parts[1] != os.Getenv("GITHUB_RUN_ATTEMPT")) {
		fmt.Fprintln(os.Stderr, "current run does not own pool")
		return 1
	}
	evidenceBase := dir
	dir, err = acceptance.EvidenceDirectory(dir, parts[0], parts[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	l := acceptance.Ledger{SchemaVersion: 1, Project: project, Region: region, GroupID: group, RunID: parts[0], Attempt: parts[1]}
	candidates := []string{filepath.Join(dir, "ledger.json"), filepath.Join(evidenceBase, "recovered", "ledger.json"), filepath.Join(evidenceBase, "recovered", "run-"+parts[0]+"-"+parts[1], "ledger.json")}
	for _, path := range candidates {
		if b, e := os.ReadFile(path); e == nil {
			var prior acceptance.Ledger
			if json.Unmarshal(b, &prior) == nil && prior.Project == project && prior.GroupID == group && prior.Region == region && prior.RunID == l.RunID && prior.Attempt == l.Attempt {
				l = prior
				break
			}
		}
	}

	l.Path = filepath.Join(dir, "ledger.json")
	if l.StartedAt.IsZero() {
		l.StartedAt, _ = time.Parse(time.RFC3339, g.Tag("fleeting-started-at"))
	}
	if mode == "janitor" {
		err = acceptance.ConfirmStoppedRun(ctx, &l, os.Getenv("GITHUB_TOKEN"))
	} else {
		err = acceptance.StopLocalManager(ctx, &l, dir)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	err = acceptance.Cleanup(ctx, c, &l, mode == "janitor")
	if writeErr := acceptance.WriteJSON(filepath.Join(dir, "cleanup.json"), l); writeErr != nil {
		fmt.Fprintln(os.Stderr, "cannot persist cleanup evidence")
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(l.CleanupResult)
	return 0
}
