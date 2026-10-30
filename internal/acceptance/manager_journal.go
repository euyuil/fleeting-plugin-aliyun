package acceptance

import (
	"context"
	"strconv"
	"time"
)

const maxManagerJournalBytes = 128 * 1024

// Capture after attempting the stop so diagnostics cannot delay the first
// capacity fence. This private evidence never changes the acceptance verdict.
func captureManagerJournal(result *Result, manager *packagedManager) {
	if result.ManagerLogState != "" {
		return
	}
	result.ManagerLogState = "UNAVAILABLE"
	if !manager.started {
		result.ManagerLogState = "NOT_STARTED"
		return
	}
	if result.StartedAt.IsZero() || result.StartedAt.After(time.Now()) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if manager.owned(ctx) != nil {
		return
	}
	// The freshly installed disposable service and its owner/config binding
	// restrict this window to this run, including its intentional restarts.
	raw, err := manager.command(ctx, "journalctl", "--unit="+packagedService,
		"--since=@"+strconv.FormatInt(result.StartedAt.Unix(), 10), "--output=json",
		"--output-fields=MESSAGE", "--lines=200", "--no-pager", "--quiet")
	if err != nil || len(raw) > maxManagerJournalBytes {
		return
	}
	result.ManagerLogState, result.ManagerLog = "RECORDED", string(raw)
}
