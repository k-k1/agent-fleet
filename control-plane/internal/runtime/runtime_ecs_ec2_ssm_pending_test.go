package runtime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

func pendingCmd(id, comment string, instances ...string) ssmtypes.Command {
	return ssmtypes.Command{CommandId: aws.String(id), Comment: aws.String(comment), InstanceIds: instances}
}

// The real ListCommands refuses InstanceId with Status=Pending, so Pending is listed
// account-wide and narrowed to this box and to the CP's own comment.
func TestListSlotVolumeCommandIDsPendingNarrowedToTheBox(t *testing.T) {
	f := &fakeSSMCmd{pageSize: 2, unfinished: []ssmtypes.Command{
		pendingCmd("mine-1", slotVolumeCommandComment, "i-x"),
		pendingCmd("other-box", slotVolumeCommandComment, "i-y"),
		pendingCmd("no-comment", "somebody else", "i-x"),
		pendingCmd("multi", slotVolumeCommandComment, "i-y", "i-x"),
		{CommandId: aws.String("by-target"), Comment: aws.String(slotVolumeCommandComment),
			Targets: []ssmtypes.Target{{Key: aws.String("InstanceIds"), Values: []string{"i-x"}}}},
		{CommandId: aws.String("by-tag"), Comment: aws.String(slotVolumeCommandComment),
			Targets: []ssmtypes.Target{{Key: aws.String("tag:Role"), Values: []string{"i-x"}}}},
		pendingCmd("mine-last", slotVolumeCommandComment, "i-x"),
	}}
	got, err := listSlotVolumeCommandIDs(context.Background(), f, "i-x")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"mine-1", "multi", "by-target", "mine-last"}; !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	// 7 Pending commands in pages of 2, plus one InProgress call.
	if f.listCalls != 5 {
		t.Fatalf("ListCommands reached the list %d times, want 5", f.listCalls)
	}
}

func TestListSlotVolumeCommandIDsErrorIsNotPartial(t *testing.T) {
	boom := errors.New("boom")
	f := &fakeSSMCmd{listErr: boom, unfinished: []ssmtypes.Command{pendingCmd("a", slotVolumeCommandComment, "i-x")}}
	got, err := listSlotVolumeCommandIDs(context.Background(), f, "i-x")
	if !errors.Is(err, boom) || got != nil {
		t.Fatalf("got %v, %v; want nil and the list error", got, err)
	}
}

func TestCancelSlotVolumeCommandsCancelsPendingOnlyForTheBox(t *testing.T) {
	f := &fakeSSMCmd{pageSize: 1, unfinished: []ssmtypes.Command{
		pendingCmd("other-box", slotVolumeCommandComment, "i-y"),
		pendingCmd("mine", slotVolumeCommandComment, "i-x"),
		pendingCmd("foreign", "somebody else", "i-x"),
	}}
	cancelled, failed := cancelSlotVolumeCommands(context.Background(), f, "i-x")
	if cancelled != 1 || failed != 0 || fmt.Sprint(f.cancelled) != "[mine]" {
		t.Fatalf("cancelled=%d failed=%d ids=%v", cancelled, failed, f.cancelled)
	}
}

func TestCancelSlotVolumeCommandsCountsAListFailure(t *testing.T) {
	f := &fakeSSMCmd{listErr: errors.New("boom")}
	if cancelled, failed := cancelSlotVolumeCommands(context.Background(), f, "i-x"); cancelled != 0 || failed != 1 {
		t.Fatalf("cancelled=%d failed=%d", cancelled, failed)
	}
}
