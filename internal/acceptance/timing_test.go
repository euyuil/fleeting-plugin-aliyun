package acceptance

import (
	"testing"
	"time"
)

func TestFixedJobTimingIncludesSetupAndCannotRollForward(t *testing.T) {
	started := time.Date(2026, 1, 1, 8, 0, 45, 0, time.UTC)
	for scenario, minutes := range map[string]int{"smoke": 20, "upgrade": 20, "failure": 40, "cancel": 40, "timeout": 40, "capacity": 40, "drain": 40, "worker-loss": 40, "manager-crash": 50} {
		t.Run(scenario, func(t *testing.T) {
			deadline := started.Add(time.Duration(minutes) * time.Minute)
			expires := deadline.Add(40 * time.Minute).Truncate(time.Minute)
			env := map[string]string{"ACCEPTANCE_STARTED_AT": started.Format(time.RFC3339), "ACCEPTANCE_DEADLINE_AT": deadline.Format(time.RFC3339), "ACCEPTANCE_EXPIRES_AT": expires.Format(time.RFC3339)}
			get := func(key string) string { return env[key] }
			for _, elapsed := range []time.Duration{time.Minute, time.Duration(minutes-1) * time.Minute} {
				s, d, e, err := jobTiming(get, scenario, started.Add(elapsed))
				if err != nil || !s.Equal(started) || !d.Equal(deadline) || !e.Equal(expires) {
					t.Fatalf("fixed timestamps changed after setup: %v", err)
				}
			}
			for _, key := range []string{"ACCEPTANCE_STARTED_AT", "ACCEPTANCE_DEADLINE_AT", "ACCEPTANCE_EXPIRES_AT"} {
				original := env[key]
				for _, changed := range []string{"", "invalid", started.Add(2 * time.Hour).Format(time.RFC3339)} {
					env[key] = changed
					if _, _, _, err := jobTiming(get, scenario, started.Add(time.Minute)); err == nil {
						t.Fatal("missing, malformed or expanded timing accepted", key)
					}
				}
				env[key] = original
			}
			if _, _, _, err := jobTiming(get, scenario, deadline); err == nil {
				t.Fatal("expired execution budget accepted")
			}
		})
	}
}
