// agent_dial.go — the CP→Agent connection. Only when a Service Connect alias fails to
// resolve is the name looked up again through Cloud Map (ADR: docs/build/09 §ECS).
//
// Why it exists, from what ECS actually does: a Service Connect client alias is not DNS.
// At task start the ECS agent writes `127.255.0.1 <alias>` into the task's /etc/hosts,
// and the sidecar Envoy relays that loopback address to the real endpoint. Measured: the
// af.internal private zone holds nothing but NS and SOA, on a running deployment too.
//
// So that line is written once, at task start, and a service added to the namespace
// afterwards never appears in it. CP stays up for a long time, so any workspace created
// after the CP task — the first person on a new deployment, and the first login of every
// member who joins after CP started — has no entry in /etc/hosts, falls through to the
// VPC resolver and gets NXDOMAIN:
//
//	dial tcp: lookup af-ws-… on 10.20.0.2:53: no such host
//
// Waiting does not fix it; it is neither a cache nor a TTL. Only replacing the CP task,
// which rewrites /etc/hosts, does — i.e. a CP restart for every new member.
//
// All this file adds is "ask Cloud Map when the name did not resolve". The happy path
// still goes through Envoy, so neither the route nor the behaviour changes. CpTaskRole
// already carries servicediscovery:DiscoverInstances (20-platform, Sid
// ServiceConnectDiscovery), so no permission has to be granted either.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/servicediscovery"
)

// scDiscoverAPI is the narrow port for the Cloud Map call, in the same style as
// runtime_ecs.go's ecsAPI: the real *servicediscovery.Client satisfies it and tests
// substitute a fake.
type scDiscoverAPI interface {
	DiscoverInstances(context.Context, *servicediscovery.DiscoverInstancesInput, ...func(*servicediscovery.Options)) (*servicediscovery.DiscoverInstancesOutput, error)
}

// agentResolver resolves alias → ip:port through Cloud Map. ttl is not "how long until a
// new workspace is noticed" but "how often the lookup is repeated": the lookup only runs
// after DNS has already failed, so too short hits the API on every failure and too long
// holds a stale IP across a task replacement.
type agentResolver struct {
	api    scDiscoverAPI
	nsName string // Cloud Map namespace (e.g. af.internal); empty disables the fallback
	ttl    time.Duration

	mu    sync.Mutex
	cache map[string]resolvedAgent
}

type resolvedAgent struct {
	addr string
	exp  time.Time
}

// agentDialer is shared by every CP→Agent path. nil means a plain dial: Service Connect
// is irrelevant on the docker/native runtimes, so they get no fallback.
var agentDialer *agentResolver

var agentBaseDialer = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}

// initAgentResolver is called once, when an ECS-family runtime starts. DiscoverInstances
// takes the namespace by NAME while what we hold is an ARN, so GetNamespace translates it
// once at startup rather than on every lookup. A deployment with no namespace, or one
// that cannot be read, quietly falls back to having no fallback — the previous behaviour.
func initAgentResolver(ctx context.Context, ac aws.Config, namespaceArn string) {
	id := namespaceArn
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	if id == "" {
		return
	}
	api := servicediscovery.NewFromConfig(ac)
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := api.GetNamespace(cctx, &servicediscovery.GetNamespaceInput{Id: aws.String(id)})
	if err != nil || out.Namespace == nil {
		log.Printf("agent dial: 名前空間 %s の名前を引けなかったのでフォールバックは無効: %v", id, err)
		return
	}
	setAgentResolver(api, aws.ToString(out.Namespace.Name))
}

// setAgentResolver is called once, when an ECS-family runtime starts.
func setAgentResolver(api scDiscoverAPI, namespaceName string) {
	if api == nil || strings.TrimSpace(namespaceName) == "" {
		return
	}
	agentDialer = &agentResolver{api: api, nsName: namespaceName, ttl: 30 * time.Second, cache: map[string]resolvedAgent{}}
	log.Printf("agent dial: Service Connect の別名が引けないときは Cloud Map(%s) で引き直す", namespaceName)
}

// dialAgent is the DialContext every client and dialer here uses.
func dialAgent(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := agentBaseDialer.DialContext(ctx, network, addr)
	if err == nil || agentDialer == nil || !isDNSNotFound(err) {
		return c, err
	}
	alt, ok := agentDialer.lookup(ctx, addr)
	if !ok {
		// Return the ORIGINAL error: a fallback that found nothing must not hide the
		// real "the name does not resolve" diagnosis.
		return nil, err
	}
	return agentBaseDialer.DialContext(ctx, network, alt)
}

// isDNSNotFound matches "the name does not exist" and nothing else. Taking timeouts or
// connection refusals too would hammer Cloud Map for an Agent that is merely down.
func isDNSNotFound(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de) && de.IsNotFound
}

// lookup resolves the host of host:port as a Cloud Map service name and returns ip:port.
func (r *agentResolver) lookup(ctx context.Context, addr string) (string, bool) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || net.ParseIP(host) != nil {
		return "", false // a literal IP means DNS was never involved: a different failure
	}
	r.mu.Lock()
	if e, ok := r.cache[host]; ok && time.Now().Before(e.exp) {
		r.mu.Unlock()
		return e.addr, true
	}
	r.mu.Unlock()

	out, err := r.api.DiscoverInstances(ctx, &servicediscovery.DiscoverInstancesInput{
		NamespaceName: aws.String(r.nsName),
		ServiceName:   aws.String(host),
	})
	if err != nil {
		log.Printf("agent dial: Cloud Map で %s を引けなかった: %v", host, err)
		return "", false
	}
	for _, inst := range out.Instances {
		ip := inst.Attributes["AWS_INSTANCE_IPV4"]
		if ip == "" {
			continue
		}
		// Prefer the registered port, falling back to the requested one. Service
		// Connect always registers one, but an instance may have been registered by
		// hand.
		p := port
		if v := inst.Attributes["AWS_INSTANCE_PORT"]; v != "" {
			if _, err := strconv.Atoi(v); err == nil {
				p = v
			}
		}
		resolved := net.JoinHostPort(ip, p)
		r.mu.Lock()
		r.cache[host] = resolvedAgent{addr: resolved, exp: time.Now().Add(r.ttl)}
		r.mu.Unlock()
		// Reaching here means we just resolved a service that is not in this CP task's
		// /etc/hosts. Always leave a line, so an operator can later work out why
		// restarting CP "fixes" it.
		log.Printf("agent dial: %s は Service Connect の別名で引けなかった → Cloud Map の %s を使う", host, resolved)
		return resolved, true
	}
	return "", false
}

// newAgentTransport builds the Transport every CP→Agent HTTP call shares. The default
// Transport is cloned and only DialContext replaced, so the proxy settings, HTTP/2 and
// the connection-pool defaults are kept.
func newAgentTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = dialAgent
	return t
}
