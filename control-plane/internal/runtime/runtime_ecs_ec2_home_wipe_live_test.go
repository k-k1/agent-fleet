package runtime

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
)

// TestECSEC2LiveEraseHome drives an administrator's Clean home and the backup deletion
// against real EC2, on throwaway resources: a 1 GiB volume and three snapshots of it,
// tagged with a membership and a pool nothing else uses, so neither a real member's home
// nor the deployment's own sweeper can meet them. What the fakes cannot show: that the tag
// filters pick exactly these, that the erase deletes the home and its hibernation copy and
// leaves the backups, and that DeleteSnapshot takes a copy that is still being captured.
//
// No slot and no service are involved: the volume is never attached, and the cluster is
// only asked about a service that does not exist. The attached path (unmount over SSM,
// detach) is releaseSlot, which Destroy and hibernation already run in production.
//
// With AF_ECS_EFS_ID it also creates a keep access point for the throwaway membership (no
// mount, so no directory is ever made) and checks the erase record: that EraseHome writes
// it, and that the restore path matches it against the VolumeId EBS reports on a snapshot.
//
// Opt-in; it bills a few minutes of a 1 GiB volume and its snapshots:
//
//	AF_ECS_EC2_LIVE_ERASE=1 AF_ECS_REGION=<region> AF_ECS_CLUSTER=<cluster> \
//	  AF_ECS_EC2_LIVE_AZ=<az> [AF_ECS_EFS_ID=<fs>] go test -run TestECSEC2LiveEraseHome -v ./internal/runtime/
func TestECSEC2LiveEraseHome(t *testing.T) {
	if os.Getenv("AF_ECS_EC2_LIVE_ERASE") != "1" {
		t.Skip("set AF_ECS_EC2_LIVE_ERASE=1 with AF_ECS_REGION, AF_ECS_CLUSTER and AF_ECS_EC2_LIVE_AZ to run")
	}
	region, cluster, az := os.Getenv("AF_ECS_REGION"), os.Getenv("AF_ECS_CLUSTER"), os.Getenv("AF_ECS_EC2_LIVE_AZ")
	if region == "" || cluster == "" || az == "" {
		t.Fatal("AF_ECS_REGION, AF_ECS_CLUSTER and AF_ECS_EC2_LIVE_AZ are all required")
	}
	ctx := context.Background()
	useCPTaskRole(t)

	// The product under test, on the clients the product would build.
	pac, err := AWSConfigFor(ctx, region)
	if err != nil {
		t.Fatalf("product aws config: %v", err)
	}
	sfx := time.Now().UTC().Format("20060102150405")
	membership, pool, name := "live-erase-"+sfx, "af-live-erase-"+sfx, "af-ws-live-erase-"+sfx
	fsID := os.Getenv("AF_ECS_EFS_ID")
	rt := &ecsEC2Runtime{
		base: &ecsRuntime{cfg: ecsConfig{region: region, cluster: cluster, efsFileSystem: fsID},
			ecs: ecs.NewFromConfig(pac), efs: efs.NewFromConfig(pac), name: name, membershipID: membership},
		ec2:   ec2.NewFromConfig(pac),
		pool:  ec2PoolConfig{pool: pool},
		now:   time.Now,
		sleep: sleepCtx,
		bg:    func(context.Context, func(context.Context)) {},
	}

	// The test's own eyes and cleanup keep the ambient (deployer) credentials.
	ac, err := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(region))
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	eye := ec2.NewFromConfig(ac)
	efsEye := efs.NewFromConfig(ac)
	mine := []ec2types.Filter{tagFilter(EC2TagMembership, membership)}
	var keepAP string
	if fsID == "" {
		t.Log("NOT VERIFIED: the erase mark (set AF_ECS_EFS_ID to create a throwaway keep access point)")
	} else {
		out, err := efsEye.CreateAccessPoint(ctx, &efs.CreateAccessPointInput{
			FileSystemId: aws.String(fsID),
			RootDirectory: &efstypes.RootDirectory{Path: aws.String("/home-keep/" + membership),
				CreationInfo: &efstypes.CreationInfo{OwnerUid: aws.Int64(1000), OwnerGid: aws.Int64(1000), Permissions: aws.String("0700")}},
			Tags: []efstypes.Tag{{Key: aws.String("af-membership"), Value: aws.String(membership)},
				{Key: aws.String("af-role"), Value: aws.String("keep-ec2")}},
		})
		if err != nil {
			t.Fatalf("create the throwaway keep access point: %v", err)
		}
		keepAP = aws.ToString(out.AccessPointId)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if keepAP != "" {
			t.Logf("cleanup: deleting the throwaway keep access point %s", keepAP)
			_, _ = efsEye.DeleteAccessPoint(c, &efs.DeleteAccessPointInput{AccessPointId: aws.String(keepAP)})
		}
		if out, err := eye.DescribeSnapshots(c, &ec2.DescribeSnapshotsInput{OwnerIds: []string{"self"}, Filters: mine}); err == nil {
			for _, s := range out.Snapshots {
				t.Logf("cleanup: deleting leftover snapshot %s", aws.ToString(s.SnapshotId))
				_, _ = eye.DeleteSnapshot(c, &ec2.DeleteSnapshotInput{SnapshotId: s.SnapshotId})
			}
		}
		if out, err := eye.DescribeVolumes(c, &ec2.DescribeVolumesInput{Filters: mine}); err == nil {
			for _, v := range out.Volumes {
				t.Logf("cleanup: deleting leftover volume %s", aws.ToString(v.VolumeId))
				_, _ = eye.DeleteVolume(c, &ec2.DeleteVolumeInput{VolumeId: v.VolumeId})
			}
		}
	})
	tags := func(role string) []ec2types.Tag {
		return []ec2types.Tag{
			{Key: aws.String(EC2TagMembership), Value: aws.String(membership)},
			{Key: aws.String(EC2TagRole), Value: aws.String(role)},
			{Key: aws.String(EC2TagWorkspace), Value: aws.String(name)},
			{Key: aws.String(EC2TagPool), Value: aws.String(pool)},
			{Key: aws.String(EC2TagBackupAt), Value: aws.String(time.Now().UTC().Format(time.RFC3339Nano))},
		}
	}

	vol, err := eye.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String(az), Size: aws.Int32(1), VolumeType: ec2types.VolumeTypeGp3, Encrypted: aws.Bool(true),
		TagSpecifications: []ec2types.TagSpecification{{ResourceType: ec2types.ResourceTypeVolume, Tags: tags(ec2RoleHome)}},
	})
	if err != nil {
		t.Fatalf("create the throwaway home: %v", err)
	}
	volID := aws.ToString(vol.VolumeId)
	if err := rt.waitVolumeAttachable(ctx, volID); err != nil {
		t.Fatalf("throwaway home never became available: %v", err)
	}
	snapshot := func(role string) string {
		out, err := eye.CreateSnapshot(ctx, &ec2.CreateSnapshotInput{VolumeId: aws.String(volID),
			TagSpecifications: []ec2types.TagSpecification{{ResourceType: ec2types.ResourceTypeSnapshot, Tags: tags(role)}}})
		if err != nil {
			t.Fatalf("snapshot (%s): %v", role, err)
		}
		return aws.ToString(out.SnapshotId)
	}
	waitCompleted := func(id string) {
		for i := 0; i < 90; i++ {
			out, err := eye.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{SnapshotIds: []string{id}})
			if err == nil && len(out.Snapshots) == 1 && out.Snapshots[0].State == ec2types.SnapshotStateCompleted {
				return
			}
			time.Sleep(5 * time.Second)
		}
		t.Fatalf("snapshot %s did not complete in time", id)
	}
	hib := snapshot(ec2RoleHome)
	waitCompleted(hib)
	backup1 := snapshot(ec2RoleBackup)
	waitCompleted(backup1)
	backup2 := snapshot(ec2RoleBackup) // left to run: it may still be pending when it is deleted
	if got, err := rt.restoreSnapshot(ctx); err != nil || got != hib {
		t.Fatalf("setup: the product would restore %q (err %v), want the hibernation copy %s", got, err, hib)
	}

	// --- the administrator's Clean home ---
	t0 := time.Now()
	if err := rt.EraseHome(ctx); err != nil {
		t.Fatalf("EraseHome: %v", err)
	}
	t.Logf("MEASURED EraseHome of a detached home = %.1fs", time.Since(t0).Seconds())
	if v, err := rt.homeVolume(ctx); err != nil || v != nil {
		t.Errorf("the home volume survived the erase (%v, err %v)", v, err)
	}
	exists := func(id string) bool {
		out, err := eye.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{OwnerIds: []string{"self"}, Filters: mine})
		if err != nil {
			t.Fatalf("describe snapshots: %v", err)
		}
		for _, s := range out.Snapshots {
			if aws.ToString(s.SnapshotId) == id {
				return true
			}
		}
		return false
	}
	// A deleted snapshot can linger in DescribeSnapshots for a moment.
	gone := func(id string) bool {
		for i := 0; i < 15; i++ {
			if !exists(id) {
				return true
			}
			time.Sleep(2 * time.Second)
		}
		return false
	}
	if !gone(hib) {
		t.Errorf("the hibernation snapshot %s survived: the next Start would restore the erased home", hib)
	}
	if !exists(backup1) || !exists(backup2) {
		t.Fatalf("the erase took a backup (backup1 kept=%v, backup2 kept=%v); only the separate deletion may", exists(backup1), exists(backup2))
	}
	if got, err := rt.restoreSnapshot(ctx); err != nil || got != "" {
		t.Errorf("after the erase the next Start would restore %q (err %v), want a fresh home", got, err)
	}

	// --- the erase record, against the VolumeId EBS reports ---
	if keepAP != "" {
		apID, rec, err := rt.ensureKeepAccessPoint(ctx)
		if err != nil || !rec.isErased(volID) || len(rec.pending) != 0 || apID != keepAP {
			t.Fatalf("EraseHome did not record %s as erased on %s: %q on %s, %v", volID, keepAP, rec.String(), apID, err)
		}
		t.Logf("erase record on %s: %q", keepAP, rec.String())
		vol2, err := eye.CreateVolume(ctx, &ec2.CreateVolumeInput{
			AvailabilityZone: aws.String(az), Size: aws.Int32(1), VolumeType: ec2types.VolumeTypeGp3, Encrypted: aws.Bool(true),
			TagSpecifications: []ec2types.TagSpecification{{ResourceType: ec2types.ResourceTypeVolume, Tags: tags("scratch")}},
		})
		if err != nil {
			t.Fatalf("create the second throwaway volume: %v", err)
		}
		if err := rt.waitVolumeAttachable(ctx, aws.ToString(vol2.VolumeId)); err != nil {
			t.Fatalf("second throwaway volume never became available: %v", err)
		}
		volID = aws.ToString(vol2.VolumeId)
		// A hibernation of a home created after the erase names another volume: restored.
		fresh := snapshot(ec2RoleHome)
		waitCompleted(fresh)
		if got, err := rt.restoreSnapshot(ctx); err != nil || got != fresh {
			t.Fatalf("a copy of a volume that was not erased: restoreSnapshot = %q, %v; want %s", got, err, fresh)
		}
		// Once that volume is recorded as erased, the same copy is refused and deleted.
		rec.markPending([]string{volID})
		rec.confirmPending()
		if err := rt.writeEraseRecord(ctx, keepAP, &rec, nil); err != nil {
			t.Fatalf("writeEraseRecord: %v", err)
		}
		if got, err := rt.restoreSnapshot(ctx); err != nil || got != "" {
			t.Fatalf("a copy of an erased volume: restoreSnapshot = %q, %v; want none", got, err)
		}
		if !gone(fresh) {
			t.Errorf("the copy of the erased volume was left billing")
		}
	}

	// --- the separate, deliberate deletion of the backups ---
	b, err := rt.HomeBackups(ctx)
	if err != nil || b.Count != 2 || b.Newest.IsZero() {
		t.Fatalf("HomeBackups = %+v, %v; want the two copies with a newest time", b, err)
	}
	if out, err := eye.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{SnapshotIds: []string{backup2}}); err == nil && len(out.Snapshots) == 1 {
		t.Logf("backup2 is %s when it is deleted", out.Snapshots[0].State)
	}
	n, err := rt.DeleteHomeBackups(ctx)
	if err != nil || n != 2 {
		t.Fatalf("DeleteHomeBackups = %d, %v; want 2, nil", n, err)
	}
	if !gone(backup1) || !gone(backup2) {
		t.Errorf("a backup survived its deletion (backup1 gone=%v, backup2 gone=%v)", gone(backup1), gone(backup2))
	}
	if b, err := rt.HomeBackups(ctx); err != nil || b.Count != 0 {
		t.Errorf("after deleting: %+v, %v; want none", b, err)
	}
}
