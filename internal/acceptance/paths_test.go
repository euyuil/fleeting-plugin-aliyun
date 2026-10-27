package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNewRunDoesNotOverwriteOldLedger(t *testing.T) {
	base := t.TempDir()
	oldDir, err := EvidenceDirectory(base, "100", "1")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(oldDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(oldDir, "ledger.json")
	old := []byte(`{"recovery":"must survive"}`)
	if err = os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_RUN_ID", "100")
	t.Setenv("GITHUB_RUN_ATTEMPT", "1")
	r := Run(context.Background(), base)
	if r.Result == "PASS" {
		t.Fatal("occupied evidence accepted")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != string(old) {
		t.Fatal("old ledger overwritten")
	}
	next, _ := EvidenceDirectory(base, "100", "2")
	if next == oldDir {
		t.Fatal("attempts share an evidence directory")
	}
}
