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
	bodies []string
	pages  []string
}

func (s *memberWireScript) Call(_ context.Context, service, action string, q map[string]string, out any) error {
	if service != "ess" || action != "DescribeScalingInstances" || q["ScalingGroupId"] != "asg-test" {
		return errors.New("unexpected API request")
	}
	index := min(len(s.pages), len(s.bodies)-1)
	s.pages = append(s.pages, q["PageNumber"])
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
