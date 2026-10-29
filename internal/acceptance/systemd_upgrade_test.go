package acceptance

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestUpgradeRequiresReleasedArtifactAndPreservedState(t *testing.T) {
	t.Chdir("../..")
	version, hash, _, err := upgradeBaseline()
	if err != nil {
		t.Fatal(err)
	}
	s, _ := fakeJobSequence(t, "smoke", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.run(ctx); err != nil {
		t.Fatal(err)
	}
	s.result.JobScenario = "upgrade"
	if verifyJobSequence(s.result) == nil {
		t.Fatal("ordinary smoke result satisfied upgrade")
	}
	good := packageUpgrade{Version: version, PackageSHA256: hash, CandidatePackageSHA256: strings.Repeat("a", 64), StatePreserved: true, RollbackVerified: true}
	s.result.Upgrade = &good
	if err := verifyJobSequence(s.result); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*packageUpgrade){
		func(p *packageUpgrade) { p.Version = "0.0.0" },
		func(p *packageUpgrade) { p.PackageSHA256 = strings.Repeat("0", 64) },
		func(p *packageUpgrade) { p.CandidatePackageSHA256 = "" },
		func(p *packageUpgrade) { p.StatePreserved = false },
		func(p *packageUpgrade) { p.RollbackVerified = false },
	} {
		bad := good
		change(&bad)
		s.result.Upgrade = &bad
		if verifyJobSequence(s.result) == nil {
			t.Fatal("incomplete or substituted package proof satisfied upgrade")
		}
	}
}
