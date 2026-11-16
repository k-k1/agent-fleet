// runtime_docker_fence.go — the docker egress fence (ADR 0122, compose addendum).
//
// With Config.DockerEgressGateway set, a workspace's per-user network is created
// --internal: docker gives it no route off the host, so a process that ignores the proxy
// variables reaches nothing. The egress proxy's container is attached to every such
// network and is the only way out. An internal network publishes no ports either, so the
// CP reaches the Agent by the container's address on that network.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// dockerAgentPort is the port the Agent listens on inside the container.
	dockerAgentPort = "7700"
	// dockerFenceLabel marks the networks the gateway has to be attached to, so the
	// reattach pass finds them without knowing the workspaces.
	dockerFenceLabel = "agent-fleet.io/egress-fence=1"
)

func (d *dockerRuntime) fenced() bool { return d.egressGateway != "" }

// dockerCLI runs the docker CLI and returns its combined output.
func (d *dockerRuntime) dockerCLI(ctx context.Context, args ...string) ([]byte, error) {
	if d.docker != nil {
		return d.docker(ctx, args...)
	}
	return execDocker(ctx, args...)
}

func execDocker(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

// publishArgs publishes the Agent's port on the host's loopback. A port published on an
// internal network is not reachable, so a fenced container gets no mapping and the CP
// dials the address recordAgentAddr stored.
func (d *dockerRuntime) publishArgs() []string {
	if d.fenced() {
		return nil
	}
	return []string{"-p", fmt.Sprintf("127.0.0.1:%s:%s", d.agentPort, dockerAgentPort)}
}

// agentAddrPath holds the fenced container's address. It lives in dataDir, next to the
// home but outside the bind mount: a workspace that could write it would point the CP,
// and the Agent token the CP sends, at an address of its choosing.
func (d *dockerRuntime) agentAddrPath() string {
	if d.dataDir == "" {
		return ""
	}
	return filepath.Join(d.dataDir, ".agent-addr")
}

// fencedAgentAddr is the address Start recorded, "" for a container with a published
// port. The file decides, not the current setting, so a workspace started before the
// setting changed stays reachable the way it was started.
func (d *dockerRuntime) fencedAgentAddr() string {
	p := d.agentAddrPath()
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	a, err := netip.ParseAddr(strings.TrimSpace(string(b)))
	if err != nil {
		return ""
	}
	return a.String()
}

// recordAgentAddr stores the just-started container's address on its fenced network.
func (d *dockerRuntime) recordAgentAddr(ctx context.Context) error {
	if !d.fenced() {
		return nil
	}
	return d.writeAgentAddr(ctx)
}

// healAgentAddr records the address of a running fenced container that has none on file.
// A CP killed between `docker run` and recordAgentAddr leaves exactly that, and without
// the file the workspace reads as running while every call to it goes to a port nothing
// publishes. A container on a plain network, started before the fence was turned on, is
// left on its published port.
func (d *dockerRuntime) healAgentAddr(ctx context.Context) error {
	if !d.fenced() || d.network == "" || d.fencedAgentAddr() != "" {
		return nil
	}
	if d.inspectOne(ctx, "network", d.network, "{{.Internal}}") != "true" {
		return nil
	}
	return d.writeAgentAddr(ctx)
}

// dropStaleAgentAddr removes an address on file once docker says the container is gone
// (removed by hand, or the deployment was torn down around its data directory). The
// address may belong to someone else's container by then, and the CP would send this
// workspace's Agent token there. It asks docker a second time, and only when a file
// exists: State's empty answer also means "docker did not answer", which must not strand
// a running workspace.
func (d *dockerRuntime) dropStaleAgentAddr(ctx context.Context) {
	if d.fencedAgentAddr() == "" {
		return
	}
	if _, err := d.inspectAddresses(ctx, "{{.Id}}"); errors.Is(err, errDockerNoSuchObject) {
		d.clearAgentAddr()
	}
}

func (d *dockerRuntime) writeAgentAddr(ctx context.Context) error {
	p := d.agentAddrPath()
	if p == "" {
		return fmt.Errorf("docker egress fence: workspace %s has no data directory to record its address in", d.name)
	}
	format := `{{with index .NetworkSettings.Networks "` + d.network + `"}}{{.IPAddress}}{{end}}`
	out := d.inspectOne(ctx, "container", d.name, format)
	a, err := netip.ParseAddr(out)
	if err != nil {
		return fmt.Errorf("docker egress fence: container %s has no address on %s (inspect answered %q)", d.name, d.network, out)
	}
	if err := os.WriteFile(p, []byte(a.String()+"\n"), 0o600); err != nil {
		return fmt.Errorf("docker egress fence: record the address of %s: %w", d.name, err)
	}
	return nil
}

func (d *dockerRuntime) clearAgentAddr() {
	if p := d.agentAddrPath(); p != "" {
		_ = os.Remove(p)
	}
}

// ensureNetwork creates the per-user network if it does not already exist, internal when
// fenced, and attaches the egress gateway to a fenced one. A network left over in the
// other mode is replaced: reusing it would either drop the fence silently or cut an
// unfenced workspace off.
func (d *dockerRuntime) ensureNetwork(ctx context.Context) error {
	if d.network == "" {
		if d.fenced() {
			return fmt.Errorf("docker egress fence: workspace %s has no per-user network", d.name)
		}
		return nil
	}
	switch internal := d.inspectOne(ctx, "network", d.network, "{{.Internal}}"); internal {
	case "":
		if err := d.createNetwork(ctx); err != nil {
			return err
		}
	case fmt.Sprint(d.fenced()):
	default:
		d.detachAll(ctx)
		if out, err := d.dockerCLI(ctx, "network", "rm", d.network); err != nil {
			return fmt.Errorf("docker network rm %s (internal=%s, want %t): %v: %s", d.network, internal, d.fenced(), err, out)
		}
		if err := d.createNetwork(ctx); err != nil {
			return err
		}
	}
	if !d.fenced() {
		return nil
	}
	return attachDockerGateway(ctx, d.dockerCLI, d.network, d.egressGateway)
}

func (d *dockerRuntime) createNetwork(ctx context.Context) error {
	args := []string{"network", "create"}
	if d.fenced() {
		args = append(args, "--internal", "--label", dockerFenceLabel)
	}
	if out, err := d.dockerCLI(ctx, append(args, d.network)...); err != nil {
		return fmt.Errorf("docker network create %s: %v: %s", d.network, err, out)
	}
	return nil
}

// detachAll disconnects whatever is still attached to the per-user network, which docker
// requires before it removes one. Start has already removed the workspace container, so
// this is the gateway, under whatever name it had when it was attached.
func (d *dockerRuntime) detachAll(ctx context.Context) {
	out, err := d.dockerCLI(ctx, "network", "inspect", "-f", "{{range .Containers}}{{println .Name}}{{end}}", d.network)
	if err != nil {
		return
	}
	for _, name := range strings.Fields(string(out)) {
		_, _ = d.dockerCLI(ctx, "network", "disconnect", "-f", d.network, name)
	}
}

// removeNetwork drops the per-user network after Stop removed the container. Best-effort:
// ensureNetwork copes with one that stayed.
func (d *dockerRuntime) removeNetwork(ctx context.Context) {
	if d.network == "" {
		return
	}
	if d.fenced() {
		_, _ = d.dockerCLI(ctx, "network", "disconnect", "-f", d.network, d.egressGateway)
	}
	_, _ = d.dockerCLI(ctx, "network", "rm", d.network)
}

type dockerCLIFunc func(ctx context.Context, args ...string) ([]byte, error)

// attachDockerGateway connects the gateway container to network. Already attached is
// success. Any other failure is an error for the caller to surface: a fenced workspace
// without the gateway has no way out at all, the Control Plane included.
func attachDockerGateway(ctx context.Context, docker dockerCLIFunc, network, gateway string) error {
	out, err := docker(ctx, "network", "connect", network, gateway)
	if err == nil || strings.Contains(strings.ToLower(string(out)), "already exists in network") {
		return nil
	}
	return fmt.Errorf("docker egress fence: attach the egress gateway %s to %s: %v: %s",
		gateway, network, err, strings.TrimSpace(string(out)))
}

// reattachDockerGateway attaches the gateway to every fenced network it is missing from.
// Recreating the gateway container (an image update, `docker compose up`) drops all its
// attachments, and the running workspaces would stay cut off until each was restarted.
func reattachDockerGateway(ctx context.Context, docker dockerCLIFunc, gateway string) error {
	out, err := docker(ctx, "network", "ls", "--filter", "label="+dockerFenceLabel, "--format", "{{.Name}}")
	if err != nil {
		return fmt.Errorf("docker network ls: %v: %s", err, strings.TrimSpace(string(out)))
	}
	fenced := strings.Fields(string(out))
	if len(fenced) == 0 {
		return nil
	}
	out, err = docker(ctx, "inspect", "--type=container", "-f",
		"{{range $name, $_ := .NetworkSettings.Networks}}{{println $name}}{{end}}", gateway)
	if err != nil {
		return fmt.Errorf("docker inspect %s: %v: %s", gateway, err, strings.TrimSpace(string(out)))
	}
	attached := map[string]bool{}
	for _, n := range strings.Fields(string(out)) {
		attached[n] = true
	}
	var firstErr error
	for _, n := range fenced {
		if attached[n] {
			continue
		}
		if err := attachDockerGateway(ctx, docker, n, gateway); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// RunDockerEgressFence keeps the egress gateway attached to the fenced networks until ctx
// ends. A failure is logged once per change of message, not on every pass.
func RunDockerEgressFence(ctx context.Context, gateway string, every time.Duration) {
	last := ""
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		msg := ""
		if err := reattachDockerGateway(ctx, execDocker, gateway); err != nil {
			msg = err.Error()
		}
		if msg != last && ctx.Err() == nil {
			if msg != "" {
				log.Printf("docker egress fence: %s", msg)
			} else {
				log.Printf("docker egress fence: the egress gateway %s is attached again", gateway)
			}
			last = msg
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// validateDockerFence refuses, at boot, a fence that cannot work.
func validateDockerFence(mcfg Config) error {
	if mcfg.DockerEgressGateway == "" {
		return nil
	}
	if len(mcfg.EgressProxyEnv) == 0 {
		return fmt.Errorf("AF_EGRESS_DOCKER_GATEWAY is set without AF_EGRESS_PROXY_ADDR: fenced workspaces would have no way out, not even to the Control Plane")
	}
	// The gateway's container name is the only name a fenced workspace can resolve and
	// reach. A proxy address naming anything else cuts every workspace off.
	for _, kv := range mcfg.EgressProxyEnv {
		k, v, _ := strings.Cut(kv, "=")
		if !strings.HasSuffix(strings.ToLower(k), "_proxy") || strings.EqualFold(k, "no_proxy") {
			continue
		}
		if u, err := url.Parse(v); err != nil || u.Hostname() != mcfg.DockerEgressGateway {
			return fmt.Errorf("AF_EGRESS_PROXY_ADDR must name the AF_EGRESS_DOCKER_GATEWAY container (%s:<port>), not %q: a fenced workspace reaches nothing else", mcfg.DockerEgressGateway, v)
		}
	}
	// The CP dials the container's address on an internal bridge, which only a process
	// in the host's network namespace can reach. A CP that is itself a bridged container
	// (the Mac overlay) names the host by something other than loopback.
	if ip := net.ParseIP(mcfg.AgentHost); mcfg.AgentHost != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("AF_EGRESS_DOCKER_GATEWAY needs the Control Plane on the host network (WS_AGENT_HOST=%q is not loopback)", mcfg.AgentHost)
	}
	return nil
}
