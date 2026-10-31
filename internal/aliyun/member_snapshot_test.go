package aliyun_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

type memberWireScript struct {
	bodies     []string
	pages      []string
	deadlines  []time.Time
	failAt     int
	err        error
	afterFirst func()
}

func (s *memberWireScript) Call(ctx context.Context, service, action string, q map[string]string, out any) error {
	if service != "ess" || action != "DescribeScalingInstances" || q["ScalingGroupId"] != "asg-test" {
		return errors.New("unexpected API request")
	}
	index := min(len(s.pages), len(s.bodies)-1)
	s.pages = append(s.pages, q["PageNumber"])
	deadline, _ := ctx.Deadline()
	s.deadlines = append(s.deadlines, deadline)
	if len(s.pages) == 1 && s.afterFirst != nil {
		s.afterFirst()
	}
	if s.err != nil && len(s.pages) == s.failAt {
		return s.err
	}
	return json.Unmarshal([]byte(s.bodies[index]), out)
}

func memberPage(total int, rows string) string {
	return fmt.Sprintf(`{"RequestId":"request-test","TotalCount":%d,"ScalingInstances":{"ScalingInstance":[%s]}}`, total, rows)
}

func wireClient(s *memberWireScript) *aliyun.Client {
	return &aliyun.Client{API: s, Region: "cn-test-1", GroupID: "asg-test"}
}

func TestMissingMemberIdentityRetriesCompleteSnapshot(t *testing.T) {
	valid := `{"InstanceId":"i-ready","ScalingGroupId":"asg-test","LifecycleState":"InService"}`
	for _, row := range []string{
		`{"InstanceId":"","ScalingGroupId":"asg-test","LifecycleState":"Init"}`,
		`{"ScalingGroupId":"asg-test","LifecycleState":"Pending"}`,
		`{"InstanceId":"","ScalingGroupId":"asg-test","LifecycleState":"Pending"}`,
		`{"InstanceId":null,"ScalingGroupId":"asg-test","LifecycleState":"Pending"}`,
	} {
		t.Run(row, func(t *testing.T) {
			s := &memberWireScript{bodies: []string{memberPage(1, row), memberPage(1, valid)}}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			members, err := wireClient(s).Members(ctx)
			if err != nil || len(members) != 1 || members["i-ready"].ScalingGroupId != "asg-test" || !reflect.DeepEqual(s.pages, []string{"1", "1"}) {
				t.Fatalf("members=%v pages=%v err=%v", members, s.pages, err)
			}
		})
	}
}

func TestPersistentMissingMemberIdentityCannotReturnPartialSuccess(t *testing.T) {
	for _, row := range []string{
		`{"InstanceId":"i-one"}`,
		`{"InstanceId":"i-one","ScalingGroupId":null}`,
		`{"InstanceId":"i-one","ScalingGroupId":""}`,
		`null`,
	} {
		t.Run(row, func(t *testing.T) {
			s := &memberWireScript{bodies: []string{memberPage(1, row)}}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			members, err := wireClient(s).Members(ctx)
			if members != nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "missing_group_id=true") || !strings.Contains(err.Error(), "request_id=request-test") {
				t.Fatalf("members=%v err=%v", members, err)
			}
		})
	}
}

func TestForeignMemberOnLaterPageIsRejectedWithoutRetry(t *testing.T) {
	s := &memberWireScript{bodies: []string{
		memberPage(2, `{"InstanceId":"i-one","ScalingGroupId":"asg-test"}`),
		memberPage(2, `{"InstanceId":"i-two","ScalingGroupId":"asg-other"}`),
	}}
	members, err := wireClient(s).Members(context.Background())
	if members != nil || err == nil || !strings.Contains(err.Error(), "foreign member group") || !reflect.DeepEqual(s.pages, []string{"1", "2"}) {
		t.Fatalf("members=%v pages=%v err=%v", members, s.pages, err)
	}
}

func TestIdentityRetryDiscardsEarlierPages(t *testing.T) {
	s := &memberWireScript{bodies: []string{
		memberPage(2, `{"InstanceId":"i-old","ScalingGroupId":"asg-test"}`),
		memberPage(2, `{"InstanceId":"i-missing"}`),
		memberPage(2, `{"InstanceId":"i-new-a","ScalingGroupId":"asg-test"}`),
		memberPage(2, `{"InstanceId":"i-new-b","ScalingGroupId":"asg-test"}`),
	}}
	members, err := wireClient(s).Members(context.Background())
	_, stale := members["i-old"]
	if err != nil || stale || len(members) != 2 || !reflect.DeepEqual(s.pages, []string{"1", "2", "1", "2"}) {
		t.Fatalf("members=%v pages=%v err=%v", members, s.pages, err)
	}
}

func TestIdentityRetryStillRejectsDuplicateMembers(t *testing.T) {
	s := &memberWireScript{bodies: []string{
		memberPage(1, `null`),
		memberPage(2, `{"InstanceId":"i-one","ScalingGroupId":"asg-test"}`),
		memberPage(2, `{"InstanceId":"i-one","ScalingGroupId":"asg-test"}`),
	}}
	members, err := wireClient(s).Members(context.Background())
	if members != nil || err == nil || !strings.Contains(err.Error(), "duplicate member") {
		t.Fatalf("members=%v err=%v", members, err)
	}
}

func TestIncompletePaginationRestartsWithoutPublishingEarlierPages(t *testing.T) {
	old := `{"InstanceId":"i-old","ScalingGroupId":"asg-test"}`
	other := `{"InstanceId":"i-other","ScalingGroupId":"asg-test"}`
	fresh := `{"InstanceId":"i-fresh","ScalingGroupId":"asg-test"}`
	for _, tc := range []struct {
		name   string
		bodies []string
		pages  []string
		empty  bool
	}{
		{"total shrinks", []string{memberPage(2, old), memberPage(1, other), memberPage(1, fresh)}, []string{"1", "2", "1"}, false},
		{"total grows", []string{memberPage(2, old), memberPage(3, other), memberPage(1, fresh)}, []string{"1", "2", "1"}, false},
		{"early empty first page", []string{memberPage(1, ""), memberPage(0, "")}, []string{"1", "1"}, true},
		{"early empty later page", []string{memberPage(2, old), memberPage(2, ""), memberPage(1, fresh)}, []string{"1", "2", "1"}, false},
		{"rows exceed total", []string{memberPage(1, old+","+other), memberPage(1, fresh)}, []string{"1", "1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &memberWireScript{bodies: tc.bodies}
			members, err := wireClient(s).Members(context.Background())
			if err != nil || !reflect.DeepEqual(s.pages, tc.pages) {
				t.Fatalf("members=%v pages=%v err=%v", members, s.pages, err)
			}
			if tc.empty {
				if members == nil || len(members) != 0 {
					t.Fatalf("inconsistent page escaped as members: %v", members)
				}
			} else if len(members) != 1 || members["i-fresh"].ScalingGroupId != "asg-test" {
				t.Fatalf("earlier page survived a full reread: %v", members)
			}
		})
	}
}

func TestEveryMembershipPageRequiresValidCountWithoutRetry(t *testing.T) {
	one := `{"InstanceId":"i-one","ScalingGroupId":"asg-test"}`
	two := `{"InstanceId":"i-two","ScalingGroupId":"asg-test"}`
	for _, later := range []bool{false, true} {
		for _, invalid := range []string{
			`{"ScalingInstances":{"ScalingInstance":[` + two + `]}}`,
			`{"TotalCount":null,"ScalingInstances":{"ScalingInstance":[` + two + `]}}`,
			memberPage(-1, two),
		} {
			t.Run(fmt.Sprintf("later=%t/%s", later, invalid), func(t *testing.T) {
				bodies, pages := []string{invalid}, []string{"1"}
				if later {
					bodies, pages = []string{memberPage(2, one), invalid}, []string{"1", "2"}
				}
				s := &memberWireScript{bodies: append(bodies, memberPage(0, ""))}
				members, err := wireClient(s).Members(context.Background())
				if members != nil || err == nil || !strings.Contains(err.Error(), "TotalCount") || !reflect.DeepEqual(s.pages, pages) {
					t.Fatalf("members=%v pages=%v err=%v", members, s.pages, err)
				}
			})
		}
	}
}

func TestChangingCountCannotHideHardMemberViolations(t *testing.T) {
	one := `{"InstanceId":"i-one","ScalingGroupId":"asg-test"}`
	foreign := `{"InstanceId":"i-other","ScalingGroupId":"asg-other"}`
	for _, tc := range []struct{ name, rows, want string }{
		{"foreign with drift", foreign, "foreign member group"},
		{"duplicate with drift", one, "duplicate member"},
		{"missing identity before foreign", `{"ScalingGroupId":"asg-test"},` + foreign, "foreign member group"},
		{"missing group before duplicate", `{"InstanceId":"i-other"},{"InstanceId":"i-other","ScalingGroupId":"asg-test"}`, "duplicate member"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &memberWireScript{bodies: []string{memberPage(2, one), memberPage(1, tc.rows), memberPage(0, "")}}
			members, err := wireClient(s).Members(context.Background())
			if members != nil || err == nil || !strings.Contains(err.Error(), tc.want) || !reflect.DeepEqual(s.pages, []string{"1", "2"}) {
				t.Fatalf("members=%v pages=%v err=%v", members, s.pages, err)
			}
		})
	}
}

func TestInconsistentMembershipKeepsCallerDeadlineAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			s := &memberWireScript{bodies: []string{memberPage(1, "")}}
			want := context.DeadlineExceeded
			if canceled {
				s.afterFirst, want = cancel, context.Canceled
			}
			members, err := wireClient(s).Members(ctx)
			if members != nil || !errors.Is(err, want) || !strings.Contains(err.Error(), "incomplete membership pagination") || !reflect.DeepEqual(s.pages, []string{"1"}) {
				t.Fatalf("members=%v pages=%v err=%v", members, s.pages, err)
			}
		})
	}
}

func TestDifferentIncompleteSnapshotsShareOneExistingRetryWindow(t *testing.T) {
	s := &memberWireScript{bodies: []string{memberPage(1, `null`), memberPage(1, ""), memberPage(0, "")}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	before := time.Now()
	members, err := wireClient(s).Members(ctx)
	if err != nil || members == nil || len(members) != 0 || !reflect.DeepEqual(s.pages, []string{"1", "1", "1"}) {
		t.Fatalf("members=%v pages=%v err=%v", members, s.pages, err)
	}
	if s.deadlines[1] != s.deadlines[2] || !s.deadlines[1].Before(s.deadlines[0]) {
		t.Fatalf("retry deadline was reset or exceeded the parent: %v", s.deadlines)
	}
	if window := s.deadlines[1].Sub(before); window < 30*time.Second || window > 35*time.Second {
		t.Fatalf("existing 30-second window changed: %v", window)
	}
}

func TestMembershipAPIErrorsAreNotSnapshotRetries(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			original := errors.New("synthetic API read failure")
			s := &memberWireScript{bodies: []string{memberPage(1, "")}, failAt: failAt, err: original}
			members, err := wireClient(s).Members(context.Background())
			if members != nil || !errors.Is(err, original) || len(s.pages) != failAt {
				t.Fatalf("members=%v pages=%v err=%v", members, s.pages, err)
			}
		})
	}
}

func TestCompleteEmptyMembershipDoesNotNeedRetry(t *testing.T) {
	s := &memberWireScript{bodies: []string{memberPage(0, "")}}
	members, err := wireClient(s).Members(context.Background())
	if err != nil || members == nil || len(members) != 0 || !reflect.DeepEqual(s.pages, []string{"1"}) {
		t.Fatalf("members=%v pages=%v err=%v", members, s.pages, err)
	}
}
