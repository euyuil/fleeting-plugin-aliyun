package provider_test

import (
	"slices"
	"testing"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/testutil"
	upstream "gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

// Simulate ESS replacement while exercising the real provider. Runner's
// adoption and idle drain still require separate real-job acceptance.
func TestExternalLossRetainsReplacementIntentAndCanDrain(t *testing.T) {
	f := testutil.New()
	f.Group.MaxSize = 1
	g := setup(t, f)
	if n, err := g.Increase(t.Context(), 1); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	f.Add("i-injected-loss")
	states(t, g)
	delete(f.Members, "i-injected-loss")
	delete(f.Instances, "i-injected-loss")
	f.Group.TotalCapacity = 0
	if states(t, g)["i-injected-loss"] != upstream.StateDeleted {
		t.Fatal("absence not published")
	}
	ids, err := g.Decrease(t.Context(), []string{"i-injected-loss"})
	if err != nil || !slices.Equal(ids, []string{"i-injected-loss"}) || f.Group.DesiredCapacity != 1 {
		t.Fatal(ids, err, f.Group.DesiredCapacity)
	}
	for _, call := range f.Calls {
		if call.Action == "RemoveInstances" {
			t.Fatal("absent ID was removed again")
		}
	}
	if n, err := g.Increase(t.Context(), 1); n != 0 || err != nil {
		t.Fatal("existing intent must not be duplicated", n, err)
	}
	f.Add("i-ess-replacement")
	if states(t, g)["i-ess-replacement"] != upstream.StateRunning {
		t.Fatal("replacement not adopted")
	}
	if ids, err := g.Decrease(t.Context(), []string{"i-ess-replacement"}); err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
	if _, err := g.Decrease(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	states(t, g)
	if f.Group.DesiredCapacity != 0 || len(f.Instances) != 0 {
		t.Fatal("replacement did not drain")
	}
	if n, err := g.Increase(t.Context(), 1); n != 1 || err != nil {
		t.Fatal("same provider cannot accept fresh recovery demand", n, err)
	}
	f.Add("i-recovery-job")
	states(t, g)
	if ids, err := g.Decrease(t.Context(), []string{"i-recovery-job"}); err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
	if _, err := g.Decrease(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if f.Group.DesiredCapacity != 0 || len(f.Instances) != 0 {
		t.Fatal("recovery did not drain")
	}
	increases, removals := 0, 0
	for _, call := range f.Calls {
		if call.Action == "ScaleWithAdjustment" {
			increases++
		}
		if call.Action == "RemoveInstances" {
			removals++
		}
	}
	if increases != 2 || removals != 2 {
		t.Fatal("unexpected mutations", increases, removals)
	}
}
