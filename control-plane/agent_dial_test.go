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
	// 拾うのは「名前が無い」だけ。接続拒否やタイムアウトで Cloud Map を叩くと、
	// ただ落ちている Agent に対して毎回 API を打つことになる。
	if !isDNSNotFound(&net.DNSError{Err: "no such host", IsNotFound: true}) {
		t.Fatal("NXDOMAIN を拾えていない")
	}
	for _, err := range []error{
		errors.New("connection refused"),
		&net.DNSError{Err: "timeout", IsTimeout: true},
		&net.OpError{Op: "dial", Err: errors.New("connection refused")},
	} {
		if isDNSNotFound(err) {
			t.Fatalf("拾ってはいけないエラーを拾った: %v", err)
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
	// ポート属性が無い実体（手で登録されたもの）でも、要求されたポートで繋ぐ。
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
			t.Fatal("解決できていない")
		}
	}
	if f.calls != 1 {
		t.Fatalf("Cloud Map を %d 回叩いた（1 回であるべき）", f.calls)
	}
}

func TestLookupExpires(t *testing.T) {
	// タスクが入れ替われば IP は変わる。TTL を過ぎたら引き直す。
	f := &fakeDiscover{insts: []sdtypes.HttpInstanceSummary{
		inst(map[string]string{"AWS_INSTANCE_IPV4": "10.0.0.6", "AWS_INSTANCE_PORT": "7700"}),
	}}
	r := newResolver(f)
	r.ttl = -time.Second // 常に期限切れ
	r.lookup(context.Background(), "af-ws-erin:7700")
	r.lookup(context.Background(), "af-ws-erin:7700")
	if f.calls != 2 {
		t.Fatalf("calls = %d（期限切れなら毎回引き直す）", f.calls)
	}
}

func TestLookupIgnoresLiteralIP(t *testing.T) {
	// IP 直指定で dial が失敗したなら DNS は無関係。Cloud Map を叩いてはいけない。
	f := &fakeDiscover{}
	if _, ok := newResolver(f).lookup(context.Background(), "10.20.11.172:7700"); ok {
		t.Fatal("IP 直指定を引いてしまった")
	}
	if f.calls != 0 {
		t.Fatalf("calls = %d（0 であるべき）", f.calls)
	}
}

func TestLookupErrorIsNotFatal(t *testing.T) {
	f := &fakeDiscover{err: errors.New("AccessDenied")}
	if _, ok := newResolver(f).lookup(context.Background(), "af-ws-frank:7700"); ok {
		t.Fatal("失敗したのに解決したことになっている")
	}
}

func TestDialAgentWithoutResolver(t *testing.T) {
	// docker/native のランタイムでは resolver を建てない。素の dial のエラーが
	// そのまま返る（フォールバックが原因を隠さない）。
	prev := agentDialer
	agentDialer = nil
	defer func() { agentDialer = prev }()
	_, err := dialAgent(context.Background(), "tcp", "af-no-such-host.invalid:7700")
	if err == nil {
		t.Fatal("エラーになるべき")
	}
}
