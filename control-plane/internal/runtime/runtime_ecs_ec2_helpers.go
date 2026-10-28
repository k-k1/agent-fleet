package runtime

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// --- small helpers ---

func tagFilter(key, value string) ec2types.Filter {
	return ec2types.Filter{Name: aws.String("tag:" + key), Values: []string{value}}
}

func ec2TagValue(tags []ec2types.Tag, key string) string {
	for _, t := range tags {
		if aws.ToString(t.Key) == key {
			return aws.ToString(t.Value)
		}
	}
	return ""
}

func attachment(vol *ec2types.Volume) *ec2types.VolumeAttachment {
	for i := range vol.Attachments {
		switch vol.Attachments[i].State {
		case ec2types.VolumeAttachmentStateAttached, ec2types.VolumeAttachmentStateAttaching:
			return &vol.Attachments[i]
		}
	}
	return nil
}

func attachedInstance(vol *ec2types.Volume) string {
	if a := attachment(vol); a != nil {
		return aws.ToString(a.InstanceId)
	}
	return ""
}

func filterSlotsByAZ(slots []ec2SlotCandidate, az string) []ec2SlotCandidate {
	var out []ec2SlotCandidate
	for _, s := range slots {
		if s.az == az {
			out = append(out, s)
		}
	}
	return out
}

func chunkStrings(in []string, n int) [][]string {
	var out [][]string
	for len(in) > n {
		out = append(out, in[:n])
		in = in[n:]
	}
	if len(in) > 0 {
		out = append(out, in)
	}
	return out
}

// sleepCtx sleeps unless the context ends first — the poll primitive every wait above
// is built on, so a canceled Start stops polling AWS immediately.
// backgroundWithin is the production bg: detach from the request (whose cancelation
// would otherwise kill the convergence the moment the HTTP response is written) but
// keep a budget, so a wedged AWS call cannot leak a goroutine forever.
func backgroundWithin(budget time.Duration) func(context.Context, func(context.Context)) {
	return func(ctx context.Context, fn func(context.Context)) {
		go func() {
			c, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
			defer cancel()
			fn(c)
		}()
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
