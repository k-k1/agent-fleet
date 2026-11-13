package datadurable

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/datalayout"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// A trimmed real /proc/self/mountinfo of a Fargate-like task: overlay root, an EFS mount,
// tmpfs on /dev and a mount point with an escaped space.
const sampleMountInfo = `1203 1100 0:56 / / rw,relatime master:400 - overlay overlay rw,lowerdir=/a,upperdir=/b,workdir=/c
1204 1203 0:59 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw
1205 1203 0:60 / /dev rw,nosuid - tmpfs tmpfs rw,size=65536k,mode=755
1210 1203 0:70 / /var/lib/af-cp rw,relatime shared:5 master:6 - nfs4 127.0.0.1:/ rw,vers=4.1
1211 1203 0:71 / /run/shm rw,nosuid,nodev - tmpfs shm rw
1212 1203 8:1 / /mnt/my\040disk rw,relatime - ext4 /dev/sda1 rw
1213 1203 0:72 / /var/lib/af-cp/cache rw - tmpfs tmpfs rw
`

func TestParseMountInfo(t *testing.T) {
	got := ParseMountInfo(strings.NewReader(sampleMountInfo + "garbage line\n"))
	want := map[string]string{"/": "overlay", "/dev": "tmpfs", "/var/lib/af-cp": "nfs4", "/mnt/my disk": "ext4"}
	have := map[string]string{}
	for _, m := range got {
		have[m.Point] = m.FSType
	}
	for p, fs := range want {
		if have[p] != fs {
			t.Errorf("mount %q: fstype %q, want %q (all: %v)", p, have[p], fs, have)
		}
	}
	if len(got) != 7 {
		t.Errorf("parsed %d entries, want 7 (the garbage line must be skipped)", len(got))
	}
}

func TestDataRootDurability(t *testing.T) {
	mounts := ParseMountInfo(strings.NewReader(sampleMountInfo))
	cases := []struct {
		name, root string
		mounts     []Mount
		durable    bool
		why        string
	}{
		{"the ECS default /tmp", "/tmp", mounts, false, "under /tmp"},
		{"below /tmp", "/tmp/af-data", mounts, false, "under /tmp"},
		{"/tmp wins even with no mount info", "/tmp/x", nil, false, "under /tmp"},
		{"/tmpfoo is not /tmp", "/tmpfoo/data", nil, true, ""},
		{"overlay root", "/var/lib/af", mounts, false, "overlay"},
		{"tmpfs mount", "/dev/shm/data", mounts, false, "tmpfs"},
		{"EFS mount inside the overlay root", "/var/lib/af-cp", mounts, true, ""},
		{"below the EFS mount", "/var/lib/af-cp/data", mounts, true, ""},
		{"tmpfs nested in a durable mount", "/var/lib/af-cp/cache/x", mounts, false, "tmpfs"},
		{"mount point with a space", "/mnt/my disk/data", mounts, true, ""},
		{"/var/lib/af-cpx is not under /var/lib/af-cp", "/var/lib/af-cpx", mounts, false, "overlay"},
		{"unreadable mountinfo: cannot tell, so not refused", "/var/lib/af", nil, true, ""},
		{"a plain disk root", "/", []Mount{{"/", "ext4"}}, true, ""},
		{"ramfs", "/data", []Mount{{"/", "ext4"}, {"/data", "ramfs"}}, false, "ramfs"},
	}
	for _, c := range cases {
		ok, why := Durability(c.root, c.mounts, nil)
		if ok != c.durable || !strings.Contains(why, c.why) {
			t.Errorf("%s: Durability(%q) = %v, %q; want durable=%v reason containing %q", c.name, c.root, ok, why, c.durable, c.why)
		}
	}
}

func TestDataRootDurabilityFollowsSymlinks(t *testing.T) {
	// A data root that is a symlink into /tmp is not durable.
	resolve := func(string) string { return "/tmp/real" }
	if ok, _ := Durability("/var/lib/af-cp", nil, resolve); ok {
		t.Error("a root resolving into /tmp reported durable")
	}
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "lnk")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	realTarget, _ := filepath.EvalSymlinks(target)
	if got, want := ResolveExisting(filepath.Join(link, "not", "yet")), filepath.Join(realTarget, "not", "yet"); got != want {
		t.Errorf("ResolveExisting = %q, want %q (symlink followed, missing tail kept)", got, want)
	}
}

func TestRefused(t *testing.T) {
	mounts := ParseMountInfo(strings.NewReader(sampleMountInfo))
	cases := []struct {
		required, root string
		refused        bool
	}{
		{"", "/tmp", false},
		{"0", "/tmp", false},
		{"1", "/tmp", true},
		{"true", "/var/lib/af", true},
		{"1", "/var/lib/af-cp", false},
	}
	for _, c := range cases {
		if why := Refused(c.required, c.root, mounts, nil); (why != "") != c.refused {
			t.Errorf("required=%q root=%q: reason %q, refused want %v", c.required, c.root, why, c.refused)
		}
	}
}

func TestMissingGitReposListsExactlyTheMissing(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	dflt, _ := st.EnsureDefaultTenant(ctx)
	sec, err := st.CreateTenant(ctx, "security", "Security")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, r := range []struct {
		t    store.Tenant
		name string
		disk bool
	}{{dflt, "alive", true}, {dflt, "lost", false}, {sec, "alive", true}, {sec, "gone", false}} {
		if err := st.CreateGitRepo(ctx, store.GitRepo{ID: store.NewID(), TenantID: r.t.ID, Name: r.name, DefaultBranch: "main", CreatedAt: store.NowTS()}); err != nil {
			t.Fatal(err)
		}
		if r.disk {
			if err := os.MkdirAll(datalayout.GitRepoDir(root, r.t.Slug, r.name), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := MissingGitRepos(ctx, st, root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "default/lost,security/gone" {
		t.Errorf("missing = %v, want [default/lost security/gone]", got)
	}
	// Nothing may be deleted by the scan.
	if repos, _ := st.ListGitReposByTenant(ctx, dflt.ID); len(repos) != 2 {
		t.Errorf("the ledger lost rows: %v", repos)
	}
}
