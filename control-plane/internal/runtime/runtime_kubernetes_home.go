// runtime_kubernetes_home.go — the home operations of the kubernetes adapter (ADR 0106
// decision 4): a member's Recreate and Clean home, an administrator's Clean home, and
// growing the home claim.
//
// Nothing here runs as root and nothing here reaches the home from the CP: the removal
// runs inside the cluster, in a container from the workspace image running as dev, under
// the namespace's `restricted` level. The command is built here from homeKeep, as
// homeWipeCommand builds ecs-ec2's, so the keep list has one source.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// kubeAnnWipeGen counts the member wipes ever requested for the workspace;
	// kubeAnnWipePrefix+<kind> holds the generation of the latest request of that kind.
	// One annotation per kind, never rewritten downwards, so a Recreate requested after a
	// Clean home cannot turn the pending Clean home into a Recreate.
	kubeAnnWipeGen    = "agent-fleet.io/home-wipe-generation"
	kubeAnnWipePrefix = "agent-fleet.io/home-wipe-"

	kubeWipeContainer = "home-wipe"
	// kubeWipeRecordPath is where the wipe records the generation of each kind it has
	// carried out: a subPath of the state claim that the workspace container does not
	// mount. ADR 0106 decision 4 puts the record in the home; it is kept off the home so
	// that a member deleting or editing a file there cannot make a pod restart remove
	// their work again.
	kubeWipeRecordPath    = "/var/lib/af/wipe"
	kubeWipeRecordSubPath = "wipe"

	kubeRoleErase = "erase"

	// The home is a directory of the home claim, not the claim's root. The kubelet leaves
	// the root of a claim mounted with fsGroup as root:dev, mode 2775 (measured on GKE),
	// and only its owner or root can change that; a pod under `restricted` is neither. A
	// group-writable home fails the private-directory check of af-gcloud-exec and
	// af-aws-exec (ADR 0107, cloudexec.PrivateDir). The directory is created by the
	// home-layout init container (homeLayoutScript), running as dev, before anything
	// mounts it through subPath: a subPath the kubelet has to create itself is root's again.
	kubeHomeSubPath = ".af-home"
	// kubeHomeVolumePath is where the layout step and the erase pod mount the claim's root.
	kubeHomeVolumePath  = "/var/lib/af/home-volume"
	kubeLayoutContainer = "home-layout"
)

func (k *kubeRuntime) erasePodName() string { return k.base + "-erase" }

func wipeGenOf(ann map[string]string, key string) int64 {
	n, _ := strconv.ParseInt(ann[key], 10, 64)
	return n
}

// WipeHome records the wipe on the StatefulSet and returns; the next Start adds an init
// container that carries it out before the agent starts (addHomeWipe). That Start requires
// the settled stop, so nothing runs on the home while it is removed, and the request
// returns well inside the ingress timeout whatever the size of the home.
//
// No StatefulSet means no pod has ever run on the claims: there is nothing to remove.
func (k *kubeRuntime) WipeHome(ctx context.Context, what HomeWipe) error {
	if what != HomeWipeRepos && what != HomeWipeClean {
		return fmt.Errorf("unknown home wipe %q", what)
	}
	for attempt := 0; ; attempt++ {
		s, err := k.getStatefulSet(ctx)
		if err != nil {
			return fmt.Errorf("kubernetes wipe %s: %w", k.base, err)
		}
		if s == nil {
			return nil
		}
		gen := wipeGenOf(s.Metadata.Annotations, kubeAnnWipeGen) + 1
		patch := map[string]any{"metadata": map[string]any{
			"resourceVersion": s.Metadata.ResourceVersion,
			"annotations": map[string]string{
				kubeAnnWipeGen:                   strconv.FormatInt(gen, 10),
				kubeAnnWipePrefix + string(what): strconv.FormatInt(gen, 10),
			},
		}}
		err = k.c.mergePatch(ctx, k.stsPath(), patch, nil)
		if err == nil {
			return nil
		}
		// The controller's status writes move the resource version too; read again.
		if !isKubeConflict(err) || attempt >= 4 {
			return fmt.Errorf("kubernetes wipe %s: %w", k.base, err)
		}
	}
}

// homeLayoutContainer is the init container that runs homeLayoutScript before every
// other container of the pod, in the agent's image and restricted security settings.
func homeLayoutContainer(main kContainer) kContainer {
	return kContainer{
		Name:            kubeLayoutContainer,
		Image:           main.Image,
		ImagePullPolicy: main.ImagePullPolicy,
		Command:         []string{"/bin/sh", "-c", homeLayoutScript(kubeHomeVolumePath, kubeWipeRecordPath)},
		Resources:       main.Resources,
		VolumeMounts: []kVolumeMount{
			{Name: "home", MountPath: kubeHomeVolumePath},
			{Name: "state", MountPath: kubeWipeRecordPath, SubPath: kubeWipeRecordSubPath},
		},
		SecurityContext:          main.SecurityContext,
		TerminationMessagePolicy: "FallbackToLogsOnError",
	}
}

// homeLayoutScript makes vol/kubeHomeSubPath the home: a directory owned by dev and
// writable by nobody else. On a claim that predates the subdirectory — the home was the
// claim's root — it first moves every entry of the root into it, so an existing home keeps
// its files. lost+found stays where mkfs put it: it is root's and not dev's to move.
//
// Which layout a claim has is recorded in record/layout (none, moving, done), on the state
// claim beside the wipe record, where the member cannot reach it: nothing at the claim's
// root can say so, since in the earlier layout every name there was the member's to create.
// So a .af-home or .af-home.new the member made is refused before anything moves, not
// taken for the migrated home or its staging directory, and a claim recorded as migrated
// whose root holds anything besides the home is refused too: a pod of an earlier version
// has used the root as the home again (deploy/kubernetes/README.md, "Rolling back").
//
// The moves go into the .new directory renamed into place last, so a pod stopped halfway
// carries on at the next start and the home never appears with half of its files. An entry
// that cannot be moved, or would land on one already there, stops the pod: the alternative
// is a home that silently lacks it. A read-only directory is given owner write for its
// move and has it taken back after; a pod stopped between the two leaves it writable,
// which loses nothing.
//
// A recursive fsGroup change (the kubelet makes one whenever the root does not match) sets
// group write on everything below the root. Group write is removed again at every start
// from the home and from the directories above the Agent's state (paths.AgentStateDir),
// which cloudexec.PrivateDir walks; the member's other files are left as they are.
func homeLayoutScript(vol, record string) string {
	h := vol + "/" + kubeHomeSubPath
	return strings.Join([]string{
		"set -eu",
		"V=" + shellQuote(vol) + "; H=" + shellQuote(h) + "; N=" + shellQuote(h+".new") + "; R=" + shellQuote(record) + `; L="$R/layout"`,
		`fail() { echo "home layout: $*" >&2; exit 1; }`,
		`[ -d "$R" ] && [ ! -L "$R" ] || fail "the record directory $R is missing or not a directory, so the home's layout is unknown"`,
		`[ -L "$L" ] && fail "$L is a symbolic link"`,
		`st=none; if [ -e "$L" ]; then [ -f "$L" ] || fail "$L is not a regular file"; st=$(cat -- "$L") || fail "cannot read $L"; fi`,
		`case "$st" in none|moving|done) ;; *) fail "$L holds '$st'";; esac`,
		`put() { [ -L "$L.tmp" ] && fail "$L.tmp is a symbolic link"; echo "$1" > "$L.tmp" && mv -fT -- "$L.tmp" "$L"; }`,
		`if [ "$st" = none ]; then`,
		`  for p in "$H" "$N"; do if [ -e "$p" ] || [ -L "$p" ]; then fail "the home to migrate already holds ${p##*/}; rename it and start again"; fi; done`,
		`  put moving; st=moving`,
		`fi`,
		`if [ "$st" = moving ]; then`,
		// The home exists only once every entry has moved: the last step renames it in.
		`  if [ ! -e "$H" ] && [ ! -L "$H" ]; then`,
		`    [ -L "$N" ] && fail "$N is a symbolic link"`,
		`    mkdir -p -- "$N"`,
		`    for e in "$V"/.[!.]* "$V"/..?* "$V"/*; do`,
		`      [ -e "$e" ] || [ -L "$e" ] || continue`,
		`      n=${e##*/}`,
		`      case "$n" in ` + shellQuote(kubeHomeSubPath+".new") + `|lost+found) continue;; esac`,
		`      if [ -e "$N/$n" ] || [ -L "$N/$n" ]; then fail "both $e and $N/$n exist"; fi`,
		// Moving a directory to another parent rewrites its "..", which needs write
		// permission on the directory itself (rename(2)); Go's module cache is read-only.
		`      ro=; if [ -d "$e" ] && [ ! -L "$e" ] && [ ! -w "$e" ]; then chmod u+w -- "$e" || fail "cannot move $e into the home"; ro=1; fi`,
		`      mv -T -- "$e" "$N/$n" || fail "cannot move $e into the home"`,
		`      [ -z "$ro" ] || chmod u-w -- "$N/$n"`,
		`    done`,
		`    mv -T -- "$N" "$H"`,
		`  fi`,
		`  put done`,
		`fi`,
		`[ -d "$H" ] && [ ! -L "$H" ] || fail "$H is missing or not a directory, though the home was migrated"`,
		`for e in "$V"/.[!.]* "$V"/..?* "$V"/*; do`,
		`  [ -e "$e" ] || [ -L "$e" ] || continue`,
		`  case "${e##*/}" in ` + shellQuote(kubeHomeSubPath) + `|lost+found) ;; *) fail "the claim's root holds $e besides the home: an earlier version used the root as the home after the migration (see 'Rolling back' in deploy/kubernetes/README.md)";; esac`,
		`done`,
		// A home of another owner could only be a subPath the kubelet created; dev cannot
		// change it, and refusing to start would take the whole workspace away for what
		// costs only the cloud wrappers, which say why themselves.
		// The walk stops at the first link or foreign directory: past a link, the paths
		// below lead out of the home or into the member's own files.
		`for p in "$H" "$H/.local" "$H/.local/state" "$H/.local/state/agent-fleet"; do`,
		`  [ -d "$p" ] && [ ! -L "$p" ] || break`,
		`  if [ "$(stat -c %u -- "$p")" = "$(id -u)" ]; then chmod go-w -- "$p"; else echo "home layout: $p is not owned by $(id -u); af-gcloud-exec and af-aws-exec will refuse it" >&2; break; fi`,
		`done`,
	}, "\n")
}

// addHomeWipe adds the wipe init container when the StatefulSet carries a wipe mark.
// The container stays in every later template: a pod finds in the record which
// generations were carried out and removes nothing for them, so the mark never has to be
// cleared by a template change (which would roll the pod).
func (k *kubeRuntime) addHomeWipe(tmpl *kPodTemplateSpec, ann map[string]string) {
	repos := wipeGenOf(ann, kubeAnnWipePrefix+string(HomeWipeRepos))
	clean := wipeGenOf(ann, kubeAnnWipePrefix+string(HomeWipeClean))
	if repos == 0 && clean == 0 {
		return
	}
	main := tmpl.Spec.Containers[0]
	tmpl.Spec.InitContainers = append(tmpl.Spec.InitContainers, kContainer{
		Name:            kubeWipeContainer,
		Image:           main.Image,
		ImagePullPolicy: main.ImagePullPolicy,
		Command:         []string{"/bin/sh", "-c", homeWipeScript(kubeHomePath, kubeWipeRecordPath)},
		Env: []kEnvVar{
			{Name: "AF_WIPE_REPOS", Value: strconv.FormatInt(repos, 10)},
			{Name: "AF_WIPE_CLEAN", Value: strconv.FormatInt(clean, 10)},
		},
		Resources: main.Resources,
		VolumeMounts: []kVolumeMount{
			{Name: "home", MountPath: kubeHomePath, SubPath: kubeHomeSubPath},
			{Name: "state", MountPath: kubeWipeRecordPath, SubPath: kubeWipeRecordSubPath},
		},
		SecurityContext:          main.SecurityContext,
		TerminationMessagePolicy: "FallbackToLogsOnError",
	})
}

// homeCleanCommand removes everything at the top of the home but the homeKeep names,
// whatever each entry is: a keep file a tool replaced since the last boot is a plain file
// in the home until the entrypoint moves it back, and it has to survive too.
func homeCleanCommand(home string) string {
	keep := make([]string, 0, len(homeKeep))
	for name := range homeKeep {
		keep = append(keep, name)
	}
	slices.Sort(keep)
	var not []string
	var paths []string
	for _, name := range keep {
		not = append(not, "! -name "+shellQuote(name))
		paths = append(paths, "-path "+shellQuote(home+"/"+name))
	}
	return fmt.Sprintf(`find %s -xdev \( %s \) -prune -o %s || true; find %s -mindepth 1 -maxdepth 1 %s -exec rm -rf --one-file-system -- {} +`,
		shellQuote(home), strings.Join(paths, " -o "), homeWritableTest,
		shellQuote(home), strings.Join(not, " "))
}

// homeWritableTest is the find expression that gives the owner read, write and search on every directory it
// reaches that lacks any of them (0300 and 0600 included, not just 0555), so that rm can remove their contents: the wipes run as dev, and a read-only
// directory (Go's module cache is mode 0555) is otherwise "Permission denied", which stops
// the init container and with it the pod. chmod runs as find visits each directory, before
// it reads it, so a directory without search permission opens too. find does not follow
// symbolic links (no -L), so a link's target is never touched. It assumes no mount below
// the target, which the wipes' volume mounts do not have.
const homeWritableTest = `-type d ! -perm -u+rwx -exec chmod u+rwx {} \;`

// homeReposCleanCommand removes the home's repos directory, a missing one included.
func homeReposCleanCommand(home string) string {
	repos := shellQuote(home + "/repos")
	return "if [ -e " + repos + " ] || [ -L " + repos + " ]; then find " + repos + " -xdev " + homeWritableTest +
		" || true; rm -rf --one-file-system -- " + repos + "; fi"
}

// homeRecordFuncs are the shell functions both scripts read and write the wipe record
// with. A record is either absent — never carried out, 0 — or a regular file holding a
// number of at most 18 digits, which every sh compares without overflow. Anything else
// (a directory, a symbolic link, an empty or unreadable file, a longer number) is an
// error, never a 0: a record read as 0 repeats a Clean home at every pod restart, and a
// comparison that fails inside an `if` is just "false" to set -e, which would skip one
// wipe and run the other. The record directory itself must be a real directory.
func homeRecordFuncs(record string) []string {
	return []string{
		"R=" + shellQuote(record),
		`fail() { echo "home wipe: $*" >&2; exit 1; }`,
		`[ -d "$R" ] && [ ! -L "$R" ] || fail "the record directory $R is missing or not a directory"`,
		`num() { case "$1" in ''|*[!0-9]*) fail "$2 is not a number: '$1'";; esac; [ ${#1} -le 18 ] || fail "$2 is out of range: '$1'"; }`,
		`done_of() { f="$R/$1"; if [ -L "$f" ]; then fail "$f is a symbolic link"; elif [ -e "$f" ]; then [ -f "$f" ] || fail "$f is not a regular file"; cat -- "$f" || fail "cannot read $f"; else echo 0; fi; }`,
		`record() { [ -L "$R/$1.tmp" ] && fail "$R/$1.tmp is a symbolic link"; echo "$2" > "$R/$1.tmp" && mv -fT -- "$R/$1.tmp" "$R/$1"; }`,
	}
}

// homeWipeScript is the init container's command. It compares each requested generation
// with the one recorded as carried out and removes only what is newer; the record is
// written after the removal, so a wipe interrupted halfway is repeated, never skipped.
// Every record and request is checked before anything is removed (homeRecordFuncs).
func homeWipeScript(home, record string) string {
	return strings.Join(append(append([]string{"set -eu"}, homeRecordFuncs(record)...),
		`dc=$(done_of clean)`,
		`dr=$(done_of repos)`,
		`num "$dc" "the clean record"; num "$dr" "the repos record"; num "$AF_WIPE_CLEAN" AF_WIPE_CLEAN; num "$AF_WIPE_REPOS" AF_WIPE_REPOS`,
		`if [ "$AF_WIPE_CLEAN" -gt "$dc" ]; then `+homeCleanCommand(home)+`; record clean "$AF_WIPE_CLEAN"; fi`,
		`if [ "$AF_WIPE_REPOS" -gt "$dr" ]; then `+homeReposCleanCommand(home)+`; record repos "$AF_WIPE_REPOS"; fi`,
	), "\n")
}

// homeEraseScript is the erase pod's command: the Clean home removal, unconditionally,
// and then every pending member wipe recorded as done — the erase removed all of it. The
// record directory exists only when the state claim does; when it does, its records are
// checked before the removal like the init container's.
func homeEraseScript(home, record string) string {
	return strings.Join([]string{
		"set -eu",
		"if [ -e " + shellQuote(record) + " ] || [ -L " + shellQuote(record) + " ]; then",
		strings.Join(homeRecordFuncs(record), "\n"),
		`ec=$(done_of clean)`,
		`er=$(done_of repos)`,
		`num "$ec" "the clean record"; num "$er" "the repos record"`,
		`num "$AF_WIPE_CLEAN" AF_WIPE_CLEAN; num "$AF_WIPE_REPOS" AF_WIPE_REPOS; have_record=1`,
		"else have_record=0; fi",
		homeCleanCommand(home),
		`if [ "$have_record" = 1 ]; then [ "$AF_WIPE_CLEAN" -gt 0 ] && record clean "$AF_WIPE_CLEAN"; [ "$AF_WIPE_REPOS" -gt 0 ] && record repos "$AF_WIPE_REPOS"; fi; true`,
	}, "\n")
}

// --- an administrator's Clean home ---

// kubeErasePoll is how often EraseHome reads the erase pod while it waits.
var kubeErasePoll = time.Second

// EraseHome removes everything but homeKeep from the home now, and leaves the workspace
// stopped. It checks the settled stop itself rather than trusting the caller's State
// (`stopped` does not prove that nothing runs, ADR 0106 decision 3), runs a one-shot erase
// pod from the workspace image that mounts the home claim, waits for it within the
// caller's deadline (homeEraseBudget), reads its result and deletes it.
//
// An erase that outlives the deadline is an error and its pod keeps running: the next
// EraseHome finds it by name and waits for it, and Start refuses to launch next to it.
func (k *kubeRuntime) EraseHome(ctx context.Context) error {
	s, err := k.getStatefulSet(ctx)
	if err != nil {
		return fmt.Errorf("kubernetes erase %s: %w", k.base, err)
	}
	if s != nil {
		ok, why, err := k.stopSettled(ctx, s)
		if err != nil {
			return fmt.Errorf("kubernetes erase %s: %w", k.base, err)
		}
		if !ok {
			return fmt.Errorf("kubernetes erase %s: the workspace has not stopped (%s)", k.base, why)
		}
	}
	var home kPVC
	if err := k.c.get(ctx, k.nsPath("")+"/persistentvolumeclaims/"+k.homeClaim(), &home); err != nil {
		if isKubeNotFound(err) {
			return nil // no home yet: the next start creates an empty one
		}
		return fmt.Errorf("kubernetes erase %s: %w", k.base, err)
	}
	p, err := k.getErasePod(ctx)
	if err != nil {
		return fmt.Errorf("kubernetes erase %s: %w", k.base, err)
	}
	if p != nil && (podFinished(p) || p.Metadata.DeletionTimestamp != nil) {
		// Left by a CP that died before reading it. Its result is stale; erase again.
		if err := k.c.delete(ctx, k.podPath(p.Metadata.Name)); err != nil {
			return fmt.Errorf("kubernetes erase %s: remove the previous erase pod: %w", k.base, err)
		}
		if err := k.waitErasePodGone(ctx); err != nil {
			return err
		}
		p = nil
	}
	if p == nil {
		pod, err := k.erasePod(ctx, s)
		if err != nil {
			return fmt.Errorf("kubernetes erase %s: %w", k.base, err)
		}
		if err := k.c.create(ctx, k.nsPath("")+"/pods", pod, nil); err != nil {
			return fmt.Errorf("kubernetes erase %s: create the erase pod: %w", k.base, err)
		}
		log.Printf("kubernetes erase %s: erase pod %s started", k.base, k.erasePodName())
	}
	for {
		p, err := k.getErasePod(ctx)
		if err != nil {
			if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
				// The deadline cut the poll short: same outcome as hitting it in the select
				// below, and the caller needs to hear that the pod still runs. Any other
				// failure (a 403, a bad body) keeps its own error even if the deadline
				// passed at the same moment.
				return k.eraseStillRunning(ctx)
			}
			return fmt.Errorf("kubernetes erase %s: %w", k.base, err)
		}
		if p == nil {
			return fmt.Errorf("kubernetes erase %s: the erase pod disappeared before it finished", k.base)
		}
		if podFinished(p) {
			msg := podTerminationMessage(p)
			if err := k.c.delete(ctx, k.podPath(p.Metadata.Name)); err != nil {
				log.Printf("kubernetes erase %s: remove the finished erase pod: %v", k.base, err)
			}
			if p.Status.Phase != "Succeeded" {
				return fmt.Errorf("kubernetes erase %s: the erase pod failed: %s", k.base, msg)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return k.eraseStillRunning(ctx)
		case <-time.After(kubeErasePoll):
		}
	}
}

func (k *kubeRuntime) eraseStillRunning(ctx context.Context) error {
	return fmt.Errorf("kubernetes erase %s: %w; the erase pod %s is still running and the next erase waits for it", k.base, ctx.Err(), k.erasePodName())
}

func (k *kubeRuntime) podPath(name string) string { return k.nsPath("") + "/pods/" + name }

// getErasePod returns nil, nil when there is none.
func (k *kubeRuntime) getErasePod(ctx context.Context) (*kPod, error) {
	var p kPod
	if err := k.c.get(ctx, k.podPath(k.erasePodName()), &p); err != nil {
		if isKubeNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// podFinished reports a one-shot pod whose containers have stopped. The phase alone is
// not that: an eviction writes phase Failed before the kubelet kills the containers
// (kubelet SyncTerminatingPod sets the status first), so every container must also report
// terminated. Anything else — running, waiting, no status at all — is not finished.
func podFinished(p *kPod) bool {
	if p.Status.Phase != "Succeeded" && p.Status.Phase != "Failed" {
		return false
	}
	if len(p.Status.ContainerStatuses) == 0 {
		return false
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.State.Terminated == nil || cs.State.Running != nil || cs.State.Waiting != nil {
			return false
		}
	}
	return true
}

func podTerminationMessage(p *kPod) string {
	for _, cs := range p.Status.ContainerStatuses {
		if t := cs.State.Terminated; t != nil {
			msg := strings.TrimSpace(t.Message)
			if msg == "" {
				msg = t.Reason
			}
			return fmt.Sprintf("exit %d: %s", t.ExitCode, msg)
		}
	}
	return "phase " + p.Status.Phase
}

func (k *kubeRuntime) waitErasePodGone(ctx context.Context) error {
	for {
		p, err := k.getErasePod(ctx)
		if err != nil {
			return fmt.Errorf("kubernetes erase %s: %w", k.base, err)
		}
		if p == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("kubernetes erase %s: the previous erase pod is still being removed: %w", k.base, ctx.Err())
		case <-time.After(kubeErasePoll):
		}
	}
}

// kubeErasePodGoneBudget bounds Start's wait for a finished erase pod to disappear. A
// variable so a test can shorten it.
var kubeErasePodGoneBudget = 30 * time.Second

// clearFinishedErasePod is Start's half of the erase pod's lifecycle: a running one means
// an administrator's Clean home is under way and the workspace must not start over it;
// a finished one is deleted, and Start goes on only once the pod is gone. A pod object
// bound to a node is removed only after its kubelet has confirmed the containers are
// gone, so its absence is the proof that no rm can still run on the home, which
// ReadWriteOnce does not give: it keeps a claim on one node, not to one pod.
func (k *kubeRuntime) clearFinishedErasePod(ctx context.Context) error {
	p, err := k.getErasePod(ctx)
	if err != nil || p == nil {
		return err
	}
	if !podFinished(p) && p.Metadata.DeletionTimestamp == nil {
		return errors.New("an administrator's Clean home is still running on this workspace; start again once it has finished")
	}
	if err := k.c.delete(ctx, k.podPath(p.Metadata.Name)); err != nil {
		return err
	}
	wait, cancel := context.WithTimeout(ctx, kubeErasePodGoneBudget)
	defer cancel()
	if err := k.waitErasePodGone(wait); err != nil {
		return fmt.Errorf("the finished erase pod %s has not gone yet; start again once it has: %w", p.Metadata.Name, err)
	}
	return nil
}

// erasePod is the one-shot pod: the workspace pod's identity and security settings,
// restartPolicy Never, the home claim, and the wipe record when the state claim exists.
// The image is the one the last start ran, or the configured one pinned now.
func (k *kubeRuntime) erasePod(ctx context.Context, s *kStatefulSet) (kPod, error) {
	image := ""
	var ann map[string]string
	if s != nil && len(s.Spec.Template.Spec.Containers) > 0 {
		image = s.Spec.Template.Spec.Containers[0].Image
		ann = s.Metadata.Annotations
	}
	if image == "" {
		img, err := k.pins.resolve(ctx, k.cfg.image)
		if err != nil {
			return kPod{}, err
		}
		image = img.pinned
	}
	tmpl := k.podTemplate(image, 0, time.Now().UTC())
	main := tmpl.Spec.Containers[0]
	spec := tmpl.Spec
	spec.RestartPolicy = "Never"
	spec.ShareProcessNamespace = nil
	spec.TerminationGracePeriodSeconds = nil
	res := kResources{
		Requests: map[string]string{"cpu": "100m", "memory": "256Mi", "ephemeral-storage": "256Mi"},
		Limits:   map[string]string{"cpu": "1", "memory": "512Mi", "ephemeral-storage": "1Gi"},
	}
	// Without the state claim there is no layout record, and the layout refuses: which of
	// the claim's root and .af-home is the home cannot be told then.
	//
	// The layout runs in the erase container rather than as an init container: a failed
	// init container leaves the erase container waiting, which podFinished never reports
	// as finished. The claim's root is mounted, so the erase reaches the home through it.
	spec.InitContainers = nil
	mounts := []kVolumeMount{{Name: "home", MountPath: kubeHomeVolumePath}}
	volumes := []kVolume{{Name: "home", PersistentVolumeClaim: &kPVCVolumeSource{ClaimName: k.homeClaim()}}}
	var state kPVC
	if err := k.c.get(ctx, k.nsPath("")+"/persistentvolumeclaims/"+k.stateClaim(), &state); err == nil {
		mounts = append(mounts, kVolumeMount{Name: "state", MountPath: kubeWipeRecordPath, SubPath: kubeWipeRecordSubPath})
		volumes = append(volumes, kVolume{Name: "state", PersistentVolumeClaim: &kPVCVolumeSource{ClaimName: k.stateClaim()}})
	} else if !isKubeNotFound(err) {
		return kPod{}, err
	}
	spec.Volumes = volumes
	spec.Containers = []kContainer{{
		Name:            kubeRoleErase,
		Image:           image,
		ImagePullPolicy: main.ImagePullPolicy,
		Command: []string{"/bin/sh", "-c", homeLayoutScript(kubeHomeVolumePath, kubeWipeRecordPath) + "\n" +
			homeEraseScript(kubeHomeVolumePath+"/"+kubeHomeSubPath, kubeWipeRecordPath)},
		Env: []kEnvVar{
			{Name: "AF_WIPE_REPOS", Value: strconv.FormatInt(wipeGenOf(ann, kubeAnnWipePrefix+string(HomeWipeRepos)), 10)},
			{Name: "AF_WIPE_CLEAN", Value: strconv.FormatInt(wipeGenOf(ann, kubeAnnWipePrefix+string(HomeWipeClean)), 10)},
		},
		Resources:                res,
		VolumeMounts:             mounts,
		SecurityContext:          main.SecurityContext,
		TerminationMessagePolicy: "FallbackToLogsOnError",
	}}
	meta := k.objectMeta(k.erasePodName())
	meta.Labels[kubeLabelRole] = kubeRoleErase
	return kPod{Metadata: meta, Spec: spec}, nil
}

// --- growing the home ---

var _ interface {
	ResizeHome(context.Context) (HomeResize, error)
} = (*kubeRuntime)(nil)

// ResizeHome raises the home claim's request to the workspace's disk size. A claim cannot
// shrink, so a smaller number is reported and left for the next home this member gets.
// "growing" covers both the request just raised and an expansion still under way —
// including a stopped workspace's, whose file system grows at its next mount; "same"
// is reported only when the claim's capacity has reached the request.
func (k *kubeRuntime) ResizeHome(ctx context.Context) (HomeResize, error) {
	path := k.nsPath("") + "/persistentvolumeclaims/" + k.homeClaim()
	want := int32(k.homeGiB)
	wantBytes := int64(want) * gib
	var last error
	var haveGiB int32
	// The write is conditional on the request it read: two saves can race here (the caller
	// holds no lifecycle lease), and with RecoverVolumeExpansionFailure the API server lets
	// a request go down as long as it stays above the capacity, so an unconditional write
	// could take a larger request another save had just made back to a smaller one. A
	// refused write is decided again from what the claim says now.
	for attempt := 0; attempt < 3; attempt++ {
		var pvc kPVC
		if err := k.c.get(ctx, path, &pvc); err != nil {
			if isKubeNotFound(err) {
				return HomeResize{Outcome: HomeResizeNoHome, ToGiB: want}, nil
			}
			return HomeResize{}, err
		}
		raw := pvc.Spec.Resources.Requests["storage"]
		have, ok := quantityBytes(raw)
		if !ok {
			return HomeResize{Outcome: HomeResizeFailed, ToGiB: want, Detail: "unreadable claim size " + raw}, nil
		}
		haveGiB = int32(have / gib)
		switch {
		case wantBytes < have:
			return HomeResize{Outcome: HomeResizeShrink, FromGiB: haveGiB, ToGiB: want}, nil
		case wantBytes == have:
			capacity, _ := quantityBytes(pvc.Status.Capacity["storage"])
			if capacity < have || claimResizing(&pvc) {
				return HomeResize{Outcome: HomeResizeGrowing, FromGiB: int32(capacity / gib), ToGiB: want}, nil
			}
			return HomeResize{Outcome: HomeResizeSame, FromGiB: haveGiB, ToGiB: want}, nil
		}
		last = k.c.jsonPatch(ctx, path, []kubePatchOp{
			{Op: "test", Path: "/spec/resources/requests/storage", Value: raw},
			{Op: "replace", Path: "/spec/resources/requests/storage", Value: strconv.Itoa(int(want)) + "Gi"},
		}, nil)
		if last == nil {
			return HomeResize{Outcome: HomeResizeGrowing, FromGiB: haveGiB, ToGiB: want}, nil
		}
		if code := kubeErrCode(last); code != 409 && code != 422 {
			break
		}
	}
	// Reported, not returned, as on ecs-ec2: the quota row is written, and the usual
	// refusals (a class without allowVolumeExpansion, a quota) are fixed elsewhere.
	log.Printf("kubernetes resize %s: %d -> %d GiB: %v", k.base, haveGiB, want, last)
	return HomeResize{Outcome: HomeResizeFailed, FromGiB: haveGiB, ToGiB: want, Detail: last.Error()}, nil
}

func claimResizing(p *kPVC) bool {
	for _, c := range p.Status.Conditions {
		if (c.Type == "Resizing" || c.Type == "FileSystemResizePending") && c.Status == "True" {
			return true
		}
	}
	return false
}

// quantityBytes reads a Kubernetes quantity as bytes: the binary and decimal suffixes a
// storage size uses, and plain integers.
func quantityBytes(q string) (int64, bool) {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0, false
	}
	mult := float64(1)
	for _, u := range []struct {
		suffix string
		m      float64
	}{
		{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40}, {"Pi", 1 << 50},
		{"k", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12}, {"P", 1e15},
	} {
		if strings.HasSuffix(q, u.suffix) {
			q, mult = strings.TrimSuffix(q, u.suffix), u.m
			break
		}
	}
	f, err := strconv.ParseFloat(q, 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return int64(math.Ceil(f * mult)), true
}
