package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
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
			m.journalCommand = func(ctx context.Context, args ...string) ([]byte, bool, error) {
				raw, err := m.command(ctx, args...)
				return raw, false, err
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
			reasons := map[string]string{"": "recorded", "not-started": "not_started", "foreign-owner": "ownership_unavailable", "changed-config": "ownership_unavailable", "read-error": "read_failed", "oversize": "no_valid_records", "invalid-start": "invalid_start"}
			if result.ManagerLogCapture == nil || result.ManagerLogCapture.Reason != reasons[fault] {
				t.Fatal("capture failure reason was lost")
			}
			if (fault == "foreign-owner" || fault == "changed-config" || fault == "not-started" || fault == "invalid-start") && reads != 0 {
				t.Fatal("unowned or unstarted journal was read")
			}
		})
	}
}

func TestManagerJournalNormalizesExpandedMessagesBeforeApplyingByteLimit(t *testing.T) {
	message := "\x1b[31mERROR: ready up preparation failed err=Process exited with status 124 subsystem=taskscaler " + strings.Repeat("synthetic", 32)
	data := make([]int, len(message))
	for i := range message {
		data[i] = int(message[i])
	}
	entry, err := json.Marshal(map[string]any{"MESSAGE": data, "_HOSTNAME": "synthetic-private-host"})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(strings.Repeat(string(entry)+"\n", 200))
	if len(raw) <= maxManagerJournalBytes {
		t.Fatal("fixture does not reproduce raw expansion")
	}
	capture := &managerJournalCapture{}
	got := normalizeManagerJournal(raw, capture)
	if len(got) > maxManagerJournalBytes || strings.Contains(got, "_HOSTNAME") || capture.InvalidRecords != 0 || capture.OmittedRecords != 0 {
		t.Fatal("normal valid records were discarded or metadata retained", capture)
	}
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		var value struct {
			Message string `json:"MESSAGE"`
		}
		if json.Unmarshal([]byte(line), &value) != nil || value.Message != message {
			t.Fatal("message changed")
		}
	}
}

func TestManagerJournalRetainsWholeChronologicalTailAndMarksMissingRecords(t *testing.T) {
	var raw strings.Builder
	for i := range 200 {
		entry, _ := json.Marshal(map[string]any{"MESSAGE": strconv.Itoa(i) + strings.Repeat("x", 1000)})
		raw.Write(entry)
		raw.WriteByte('\n')
	}
	raw.WriteString("{\"MESSAGE\":null}\ninvalid\n{\"MESSAGE\":[255]}\n")
	for _, invalid := range []string{"[null,65]", "[true]", "[1.0]", "[256]", "[-1]"} {
		raw.WriteString("{\"MESSAGE\":" + invalid + "}\n")
	}
	oversized, _ := json.Marshal(map[string]string{"MESSAGE": strings.Repeat("x", maxManagerJournalBytes+1)})
	raw.Write(oversized)
	raw.WriteByte('\n')
	last, _ := json.Marshal(map[string]string{"MESSAGE": "latest cause"})
	raw.Write(last)
	raw.WriteByte('\n')
	capture := &managerJournalCapture{}
	got := normalizeManagerJournal([]byte(raw.String()), capture)
	if len(got) > maxManagerJournalBytes || capture.OmittedRecords == 0 || capture.InvalidRecords != 8 || !strings.HasSuffix(got, string(last)+"\n") {
		t.Fatal("complete tail or missing-record evidence lost", capture)
	}
	previous := -1
	lines := strings.Split(strings.TrimSpace(got), "\n")
	for _, line := range lines[:len(lines)-1] {
		var value struct {
			Message string `json:"MESSAGE"`
		}
		if json.Unmarshal([]byte(line), &value) != nil {
			t.Fatal("partial record retained")
		}
		n, err := strconv.Atoi(strings.TrimRight(value.Message, "x"))
		if err != nil || n <= previous {
			t.Fatal("chronological ordering lost")
		}
		previous = n
	}
}

func TestManagerJournalRawOutputTailIsBoundedAndSkipsPartialFirstRecord(t *testing.T) {
	var tail journalTail
	chunk := []byte(strings.Repeat("x", maxRawManagerJournalBytes))
	tail.Write(chunk)
	end := []byte("fragment\n{\"MESSAGE\":\"latest cause\"}\n")
	if n, err := tail.Write(end); n != len(end) || err != nil {
		t.Fatal("output was not drained")
	}
	if len(tail.data) != maxRawManagerJournalBytes || !tail.truncated {
		t.Fatal("raw read not bounded")
	}
	capture := &managerJournalCapture{RawTruncated: tail.truncated}
	if got := normalizeManagerJournal(tail.data, capture); got != "{\"MESSAGE\":\"latest cause\"}\n" || capture.OmittedRecords != 1 {
		t.Fatal("partial first record contaminated retained evidence", capture)
	}
}
