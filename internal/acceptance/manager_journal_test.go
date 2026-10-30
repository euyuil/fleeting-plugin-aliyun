package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestManagerJournalRequiresOwnedServiceAndBoundsPrivateEvidence(t *testing.T) {
	for _, fault := range []string{"", "not-started", "foreign-owner", "changed-config", "read-error", "oversize", "invalid-start"} {
		t.Run(fault, func(t *testing.T) {
			config := []byte("synthetic config")
			hash := sha256.Sum256(config)
			m := &packagedManager{owner: serviceOwner{Run: "1", ConfigSHA256: hex.EncodeToString(hash[:])}, started: true}
			owner, _ := json.Marshal(m.owner)
			result := Result{Result: "FAIL_PRODUCT", Ledger: Ledger{StartedAt: time.Unix(1, 0)}}
			switch fault {
			case "not-started":
				m.started = false
			case "foreign-owner":
				m.owner.Run = "2"
			case "invalid-start":
				result.StartedAt = time.Time{}
			}
			reads := 0
			m.command = func(ctx context.Context, args ...string) ([]byte, error) {
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("journal read has no deadline")
				}
				if slices.Equal(args, []string{"cat", serviceOwnerPath}) {
					return owner, nil
				}
				if slices.Equal(args, []string{"cat", packagedConfig}) {
					if fault == "changed-config" {
						return []byte("foreign configuration"), nil
					}
					return config, nil
				}
				want := []string{"journalctl", "--unit=" + packagedService, "--since=@1", "--output=json", "--output-fields=MESSAGE", "--lines=200", "--no-pager", "--quiet"}
				if !slices.Equal(args, want) {
					t.Fatal("unbounded or unrelated journal selection", args)
				}
				reads++
				if fault == "read-error" {
					return nil, errors.New("private read error")
				}
				if fault == "oversize" {
					return []byte(strings.Repeat("x", maxManagerJournalBytes+1)), nil
				}
				return []byte("{\"MESSAGE\":\"private journal\"}\n"), nil
			}
			captureManagerJournal(&result, m)
			captureManagerJournal(&result, m)
			if result.Result != "FAIL_PRODUCT" || len(result.Errors) != 0 || reads > 1 {
				t.Fatal("diagnostics changed the verdict or repeated capture")
			}
			want := "UNAVAILABLE"
			if fault == "" {
				want = "RECORDED"
			} else if fault == "not-started" {
				want = "NOT_STARTED"
			}
			if result.ManagerLogState != want || (result.ManagerLog != "") != (want == "RECORDED") {
				t.Fatal("incorrect private evidence state")
			}
			if (fault == "foreign-owner" || fault == "changed-config" || fault == "not-started" || fault == "invalid-start") && reads != 0 {
				t.Fatal("unowned or unstarted journal was read")
			}
		})
	}
}
