package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpgradeUsesCandidateCompatibleTemplateAndChecksItsInstallation(t *testing.T) {
	dir := t.TempDir()
	installed, candidate := filepath.Join(dir, "installed.toml"), filepath.Join(dir, "candidate.toml")
	old, next := []byte("baseline readiness policy"), []byte("candidate cold readiness policy")
	if err := os.WriteFile(installed, old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, next, 0600); err != nil {
		t.Fatal(err)
	}
	for _, upgrade := range []bool{false, true} {
		got, err := managerExample(installed, candidate, upgrade)
		want := old
		if upgrade {
			want = next
		}
		if err != nil || string(got) != string(want) {
			t.Fatal("wrong template source", upgrade, err)
		}
	}
	if verifyInstalledManagerExample(installed, next) == nil {
		t.Fatal("baseline or substituted template accepted as the installed candidate")
	}
	if err := os.WriteFile(installed, next, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyInstalledManagerExample(installed, next); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(candidate); err != nil {
		t.Fatal(err)
	}
	if _, err := managerExample(installed, candidate, true); err == nil {
		t.Fatal("missing candidate template accepted")
	}
	if err := os.Remove(installed); err != nil {
		t.Fatal(err)
	}
	if verifyInstalledManagerExample(installed, next) == nil {
		t.Fatal("missing installed candidate example accepted")
	}
}

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
