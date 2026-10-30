package acceptance

import (
	"errors"
	"time"
)

func jobExecutionBudget(scenario string) time.Duration {
	switch scenario {
	case "", "smoke", "upgrade":
		return 20 * time.Minute
	default:
		return 40 * time.Minute
	}
}

// The disposable-host orchestrator fixes these timestamps before creating the
// fixture. Setup consumes the same execution budget; a later driver never moves
// either deadline forward. The extra forty minutes include ECS's thirty-minute
// minimum lead time and a bounded cleanup/transmission margin.
func jobTiming(get func(string) string, scenario string, now time.Time) (started, deadline, expires time.Time, err error) {
	values := []*time.Time{&started, &deadline, &expires}
	for i, key := range []string{"ACCEPTANCE_STARTED_AT", "ACCEPTANCE_DEADLINE_AT", "ACCEPTANCE_EXPIRES_AT"} {
		*values[i], err = time.Parse(time.RFC3339, get(key))
		if err != nil {
			return started, deadline, expires, errors.New("invalid fixed job acceptance timestamps")
		}
	}
	wantDeadline := started.Add(jobExecutionBudget(scenario))
	wantExpiry := wantDeadline.Add(40 * time.Minute).UTC().Truncate(time.Minute)
	if started.After(now) || !deadline.Equal(wantDeadline) || !expires.Equal(wantExpiry) || !deadline.After(now) {
		return started, deadline, expires, errors.New("job acceptance timing exceeds its fixed scenario budget or has expired")
	}
	return started, deadline, expires, nil
}
