package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"time"
	"unicode/utf8"
)

const maxManagerJournalBytes = 128 * 1024
const maxRawManagerJournalBytes = 4 * 1024 * 1024

type managerJournalCapture struct {
	Reason         string `json:"reason"`
	RawTruncated   bool   `json:"raw_truncated"`
	OmittedRecords int    `json:"omitted_records"`
	InvalidRecords int    `json:"invalid_records"`
}

// Drain subprocess output while retaining a bounded tail. A noisy journal must
// neither block the child on stdout nor allocate its complete output in memory.
type journalTail struct {
	data      []byte
	truncated bool
}

func (t *journalTail) Write(p []byte) (int, error) {
	n := len(p)
	if n >= maxRawManagerJournalBytes {
		t.truncated = t.truncated || len(t.data) != 0 || n > maxRawManagerJournalBytes
		t.data = append(t.data[:0], p[n-maxRawManagerJournalBytes:]...)
		return n, nil
	}
	if excess := len(t.data) + n - maxRawManagerJournalBytes; excess > 0 {
		copy(t.data, t.data[excess:])
		t.data = t.data[:len(t.data)-excess]
		t.truncated = true
	}
	t.data = append(t.data, p...)
	return n, nil
}

func managerJournalCommand(ctx context.Context, args ...string) ([]byte, bool, error) {
	var tail journalTail
	err := serviceCommandOutput(ctx, 15*time.Second, &tail, args...)
	return tail.data, tail.truncated, err
}

func normalizeManagerJournal(raw []byte, capture *managerJournalCapture) string {
	if capture.RawTruncated {
		// The retained byte tail may start inside a JSON record. Omit that entire
		// first fragment; only complete records may enter the persisted window.
		if end := bytes.IndexByte(raw, '\n'); end >= 0 {
			raw = raw[end+1:]
		} else {
			raw = nil
		}
		capture.OmittedRecords++
	}
	var records [][]byte
	size := 0
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var entry struct {
			Message json.RawMessage `json:"MESSAGE"`
		}
		if !utf8.Valid(line) || json.Unmarshal(line, &entry) != nil || len(entry.Message) == 0 || bytes.Equal(entry.Message, []byte("null")) {
			capture.InvalidRecords++
			continue
		}
		var message string
		if entry.Message[0] == '"' {
			if json.Unmarshal(entry.Message, &message) != nil {
				capture.InvalidRecords++
				continue
			}
		} else {
			var values []json.RawMessage
			if entry.Message[0] != '[' || json.Unmarshal(entry.Message, &values) != nil {
				capture.InvalidRecords++
				continue
			}
			data := make([]byte, len(values))
			valid := true
			for i, value := range values {
				n, err := strconv.ParseUint(string(bytes.TrimSpace(value)), 10, 8)
				if err != nil {
					valid = false
					break
				}
				data[i] = byte(n)
			}
			if !valid || !utf8.Valid(data) {
				capture.InvalidRecords++
				continue
			}
			message = string(data)
		}
		canonical, _ := json.Marshal(struct {
			Message string `json:"MESSAGE"`
		}{message})
		canonical = append(canonical, '\n')
		if len(canonical) > maxManagerJournalBytes {
			capture.OmittedRecords++
			continue
		}
		records = append(records, canonical)
		size += len(canonical)
		for size > maxManagerJournalBytes || len(records) > 200 {
			size -= len(records[0])
			records = records[1:]
			capture.OmittedRecords++
		}
	}
	return string(bytes.Join(records, nil))
}

// Capture after attempting the stop so diagnostics cannot delay the first
// capacity fence. This private evidence never changes the acceptance verdict.
func captureManagerJournal(result *Result, manager *packagedManager) {
	if result.ManagerLogState != "" {
		return
	}
	result.ManagerLogState = "UNAVAILABLE"
	result.ManagerLogCapture = &managerJournalCapture{Reason: "ownership_unavailable"}
	if !manager.started {
		result.ManagerLogState = "NOT_STARTED"
		result.ManagerLogCapture.Reason = "not_started"
		return
	}
	if result.StartedAt.IsZero() || result.StartedAt.After(time.Now()) {
		result.ManagerLogCapture.Reason = "invalid_start"
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if manager.owned(ctx) != nil {
		return
	}
	// The freshly installed disposable service and its owner/config binding
	// restrict this window to this run, including its intentional restarts.
	read := manager.journalCommand
	if read == nil {
		read = managerJournalCommand
	}
	raw, truncated, err := read(ctx, "journalctl", "--unit="+packagedService,
		"--since=@"+strconv.FormatInt(result.StartedAt.Unix(), 10), "--output=json",
		"--output-fields=MESSAGE", "--lines=200", "--no-pager", "--quiet")
	result.ManagerLogCapture.RawTruncated = truncated
	if err != nil {
		result.ManagerLogCapture.Reason = "read_failed"
		return
	}
	result.ManagerLogCapture.Reason = "recorded"
	result.ManagerLog = normalizeManagerJournal(raw, result.ManagerLogCapture)
	if result.ManagerLog == "" && (result.ManagerLogCapture.RawTruncated || result.ManagerLogCapture.InvalidRecords > 0 || result.ManagerLogCapture.OmittedRecords > 0) {
		result.ManagerLogCapture.Reason = "no_valid_records"
		return
	}
	result.ManagerLogState = "RECORDED"
}
