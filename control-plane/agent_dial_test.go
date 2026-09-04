package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/servicediscovery"
	sdtypes "github.com/aws/aws-sdk-go-v2/service/servicediscovery/types"
)

type fakeDiscover struct {
	calls int
	insts []sdtypes.HttpInstanceSummary
	err   error
}

func (f *fakeDiscover) DiscoverInstances(context.Context, *servicediscovery.DiscoverInstancesInput, ...func(*servicediscovery.Options)) (*servicediscovery.DiscoverInstancesOutput, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &servicediscovery.DiscoverInstancesOutput{Instances: f.insts}, nil
}

func inst(attrs map[string]string) sdtypes.HttpInstanceSummary {
	return sdtypes.HttpInstanceSummary{Attributes: attrs}
}

func newResolver(f *fakeDiscover) *agentResolver {
	return &agentResolver{api: f, nsName: "af.internal", ttl: time.Minute, cache: map[string]resolvedAgent{}}
}

func TestIsDNSNotFound(t *testing.T) {
	// Only "the name does not exist" is matched. Taking connection refusals or timeouts
	// too would hammer Cloud Map for an Agent that is merely down.
	if !isDNSNotFound(&net.DNSError{Err: "no such host", IsNotFound: true}) {
		t.Fatal("NXDOMAIN was not matched")
	}
	for _, err := range []error{
		errors.New("connection refused"),
		&net.DNSError{Err: "timeout", IsTimeout: true},
		&net.OpError{Op: "dial", Err: errors.New("connection refused")},
	} {
		if isDNSNotFound(err) {
			t.Fatalf("matched an error that must not be matched: %v", err)
		}
	}
}

func TestLookupUsesRegisteredPort(t *testing.T) {
	f := &fakeDiscover{insts: []sdtypes.HttpInstanceSummary{
		inst(map[string]string{"AWS_INSTANCE_IPV4": "10.20.11.172", "AWS_INSTANCE_PORT": "7700"}),
	}}
	got, ok := newResolver(f).lookup(context.Background(), "af-ws-alice:7700")
	if !ok || got != "10.20.11.172:7700" {
		t.Fatalf("lookup = %q %v", got, ok)
	}
}

func TestLookupFallsBackToRequestedPort(t *testing.T) {
	// An instance with no port attribute (registered by hand) still connects on the
	// requested port.
	f := &fakeDiscover{insts: []sdtypes.HttpInstanceSummary{
		inst(map[string]string{"AWS_INSTANCE_IPV4": "10.0.0.9"}),
	}}
	got, ok := newResolver(f).lookup(context.Background(), "af-ws-bob:7700")
	if !ok || got != "10.0.0.9:7700" {
		t.Fatalf("lookup = %q %v", got, ok)
	}
}

func TestLookupSkipsInstancesWithoutIP(t *testing.T) {
	f := &fakeDiscover{insts: []sdtypes.HttpInstanceSummary{
		inst(map[string]string{"AWS_INSTANCE_PORT": "7700"}),
		inst(map[string]string{"AWS_INSTANCE_IPV4": "10.0.0.4", "AWS_INSTANCE_PORT": "7700"}),
	}}
	got, ok := newResolver(f).lookup(context.Background(), "af-ws-carol:7700")
	if !ok || got != "10.0.0.4:7700" {
		t.Fatalf("lookup = %q %v", got, ok)
	}
}

func TestLookupCaches(t *testing.T) {
	f := &fakeDiscover{insts: []sdtypes.HttpInstanceSummary{
		inst(map[string]string{"AWS_INSTANCE_IPV4": "10.0.0.5", "AWS_INSTANCE_PORT": "7700"}),
	}}
	r := newResolver(f)
	for i := 0; i < 3; i++ {
		if _, ok := r.lookup(context.Background(), "af-ws-dave:7700"); !ok {
			t.Fatal("not resolved")
		}
	}
	if f.calls != 1 {
		t.Fatalf("called Cloud Map %d times (must be once)", f.calls)
	}
}

func TestLookupExpires(t *testing.T) {
	// A task replacement changes the IP, so the lookup is repeated once the TTL passes.
	f := &fakeDiscover{insts: []sdtypes.HttpInstanceSummary{
		inst(map[string]string{"AWS_INSTANCE_IPV4": "10.0.0.6", "AWS_INSTANCE_PORT": "7700"}),
	}}
	r := newResolver(f)
	r.ttl = -time.Second // always expired
	r.lookup(context.Background(), "af-ws-erin:7700")
	r.lookup(context.Background(), "af-ws-erin:7700")
	if f.calls != 2 {
		t.Fatalf("calls = %d (once expired, it must look up again every time)", f.calls)
	}
}

func TestLookupIgnoresLiteralIP(t *testing.T) {
	// A dial to a literal IP that failed has nothing to do with DNS; Cloud Map must not
	// be called.
	f := &fakeDiscover{}
	if _, ok := newResolver(f).lookup(context.Background(), "10.20.11.172:7700"); ok {
		t.Fatal("looked up a literal IP")
	}
	if f.calls != 0 {
		t.Fatalf("calls = %d (must be 0)", f.calls)
	}
}

func TestLookupErrorIsNotFatal(t *testing.T) {
	f := &fakeDiscover{err: errors.New("AccessDenied")}
	if _, ok := newResolver(f).lookup(context.Background(), "af-ws-frank:7700"); ok {
		t.Fatal("reported as resolved even though the lookup failed")
	}
}

func TestDialAgentWithoutResolver(t *testing.T) {
	// The docker/native runtimes build no resolver: the plain dial's error comes back
	// unchanged, so a fallback never hides the cause.
	prev := agentDialer
	agentDialer = nil
	defer func() { agentDialer = prev }()
	_, err := dialAgent(context.Background(), "tcp", "af-no-such-host.invalid:7700")
	if err == nil {
		t.Fatal("must return an error")
	}
}
