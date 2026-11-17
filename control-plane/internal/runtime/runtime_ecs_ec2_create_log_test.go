package runtime

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// Restoring a snapshot larger than the stored disk setting yields a volume of the
// snapshot's size (EBS does not refuse); the creation log must name that size.
func TestECSEC2CreateHomeLogsTheSizeEBSCreated(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.snapshots["snap-old"] = &ec2types.Snapshot{
		SnapshotId: aws.String("snap-old"), VolumeId: aws.String("vol-gone"), VolumeSize: aws.Int32(51),
		State: ec2types.SnapshotStateCompleted, StartTime: aws.Time(time.Now().Add(-24 * time.Hour)),
		Tags: []ec2types.Tag{
			{Key: aws.String(EC2TagMembership), Value: aws.String("M-1")},
			{Key: aws.String(EC2TagRole), Value: aws.String(ec2RoleHome)},
		},
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	vol, err := h.rt.createHomeVolume(ctx, "ap-northeast-1a")
	if err != nil {
		t.Fatalf("createHomeVolume: %v", err)
	}
	if got := aws.ToInt32(h.ec2.volumes[aws.ToString(vol.VolumeId)].Size); got != 51 {
		t.Fatalf("fake volume size = %d, want 51 (the snapshot's)", got)
	}
	if out := buf.String(); !strings.Contains(out, "(51 GiB,") || strings.Contains(out, "(50 GiB,") {
		t.Errorf("creation log does not state the created size 51 GiB:\n%s", out)
	}
}
