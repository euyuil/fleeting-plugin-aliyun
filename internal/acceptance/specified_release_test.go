package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

func twoWorkers(t *testing.T) (*aliyun.Client, *acceptanceAPI, *Ledger) {
	t.Helper()
	c, a, l := fixture()
	l.Mode, l.InstanceLimit, l.CreationBudget = "cloud-scale", 2, 2
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	a.fake.Add("i-remove")
	a.fake.Add("i-keep")
	if err := ObserveBudget(context.Background(), c, l, 2, "ecs.test.small", "image-test", "cn-test-1a"); err != nil {
		t.Fatal(err)
	}
	return c, a, l
}

func removeFake(a *acceptanceAPI, id string) {
	delete(a.fake.Members, id)
	delete(a.fake.Instances, id)
	a.fake.Group.DesiredCapacity = len(a.fake.Members)
	a.fake.Group.TotalCapacity = len(a.fake.Members)
}

func TestSpecifiedReleaseObservesOnlyTargetAndRetainsOtherWorker(t *testing.T) {
	c, a, l := twoWorkers(t)
	removeFake(a, "i-remove")
	a.fake.Calls = nil
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := WaitForSpecifiedRelease(ctx, c, l, "i-remove", "i-keep", "ecs.test.small", "image-test", "cn-test-1a"); err != nil {
		t.Fatal(err)
	}
	for _, call := range a.fake.Calls {
		if !strings.HasPrefix(call.Action, "Describe") {
			t.Fatalf("release observation mutated the cloud: %s", call.Action)
		}
	}
	for _, r := range l.Instances {
		if r.DeletionVerified != (r.InstanceID == "i-remove") {
			t.Fatalf("wrong release evidence: %+v", r)
		}
	}
	if len(a.fake.Members) != 1 || a.fake.Instances["i-keep"].Status != "Running" {
		t.Fatal("retained worker changed")
	}
}

func TestSpecifiedReleaseRejectsWrongDeletionReplacementAndNoProgress(t *testing.T) {
	for _, mode := range []string{"wrong-deletion", "replacement", "no-progress"} {
		t.Run(mode, func(t *testing.T) {
			c, a, l := twoWorkers(t)
			switch mode {
			case "wrong-deletion":
				removeFake(a, "i-keep")
			case "replacement":
				removeFake(a, "i-keep")
				a.fake.Add("i-replacement")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if err := WaitForSpecifiedRelease(ctx, c, l, "i-remove", "i-keep", "ecs.test.small", "image-test", "cn-test-1a"); err == nil {
				t.Fatal("incorrect or incomplete scale-in passed")
			}
			if len(a.fake.Instances) == 0 {
				t.Fatal("observation silently used emergency deletion")
			}
		})
	}
}

type releaseResourceAPI struct {
	aliyun.API
	action string
	count  any
}

func (a releaseResourceAPI) Call(ctx context.Context, service, action string, q map[string]string, out any) error {
	if action == a.action && (q["DiskIds"] != "" || q["NetworkInterfaceId.1"] != "") {
		b, _ := json.Marshal(map[string]any{"TotalCount": a.count})
		return json.Unmarshal(b, out)
	}
	return a.API.Call(ctx, service, action, q, out)
}

func TestSpecifiedReleaseCannotCertifyRemainingOrUnknownAssociatedResources(t *testing.T) {
	for _, action := range []string{"DescribeDisks", "DescribeNetworkInterfaces"} {
		for _, count := range []any{1, nil} {
			t.Run(action+"/"+fmt.Sprint(count), func(t *testing.T) {
				c, a, l := twoWorkers(t)
				removeFake(a, "i-remove")
				c.API = releaseResourceAPI{API: c.API, action: action, count: count}
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				if err := WaitForSpecifiedRelease(ctx, c, l, "i-remove", "i-keep", "ecs.test.small", "image-test", "cn-test-1a"); err == nil {
					t.Fatal("unreleased or unknown associated resource certified")
				}
			})
		}
	}
}

func TestNormalReleasePreservesTwoWorkerCreationBudget(t *testing.T) {
	c, a, l := twoWorkers(t)
	removeFake(a, "i-remove")
	removeFake(a, "i-keep")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := WaitForNormalRelease(ctx, c, l, "ecs.test.small", "image-test", "cn-test-1a"); err != nil {
		t.Fatal(err)
	}
	for _, r := range l.Instances {
		if !r.DeletionVerified {
			t.Fatal("normal two-worker release was not verified")
		}
	}
}
