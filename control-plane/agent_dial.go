// agent_dial.go — CP→Agent の接続。Service Connect の別名が引けなかったときだけ
// Cloud Map で引き直す（ADR: docs/build/09 §ECS）。
//
// ★ なぜ要るのか（実測で分かった ECS の性質）:
// Service Connect のクライアント別名は DNS ではない。ECS エージェントがタスク起動時に
// タスクの /etc/hosts へ `127.255.0.1 <alias>` を書き、そのループバックをサイドカーの
// Envoy が受けて実体へ中継する。af.internal のプライベートゾーンには NS と SOA しか無い
// （動いているデプロイでも同じ）ことを確認済み。
//
// つまり **その行はタスク起動時に一度書かれるだけ**で、後から名前空間に増えたサービスは
// 載らない。CP は起動しっぱなしなので、CP タスクより後に作られたワークスペース
// （＝新規デプロイの 1 人目と、CP 起動後に入った新メンバーの初回）は、名前が
// /etc/hosts に無いまま VPC リゾルバへ素通りして NXDOMAIN になる:
//
//	dial tcp: lookup af-ws-… on 10.20.0.2:53: no such host
//
// 時間では直らない（キャッシュでも TTL でもない）。CP タスクを差し替えて /etc/hosts を
// 書き直させたときだけ直る——これでは新メンバーが増えるたびに CP の再起動が要る。
//
// ここでやるのは「名前が引けなかったときに Cloud Map へ聞き直す」だけ。正常系は
// 従来どおり Envoy 経由のままで、経路も挙動も変えない。CpTaskRole には
// servicediscovery:DiscoverInstances が最初から付いている（20-platform の
// Sid: ServiceConnectDiscovery）ので、権限追加も要らない。
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

// scDiscoverAPI は Cloud Map 呼び出しの narrow port（runtime_ecs.go の ecsAPI と同じ流儀）。
// 実 *servicediscovery.Client が満たし、テストは偽物を差す。
type scDiscoverAPI interface {
	DiscoverInstances(context.Context, *servicediscovery.DiscoverInstancesInput, ...func(*servicediscovery.Options)) (*servicediscovery.DiscoverInstancesOutput, error)
}

// agentResolver は別名 → ip:port を Cloud Map から引く。ttl は「増えたワークスペースに
// 気づくまでの遅れ」ではなく「引き直しの間隔」: 引くのは DNS が失敗した後だけなので、
// 短すぎると失敗するたびに API を叩き、長すぎるとタスク入れ替え後の古い IP を掴む。
type agentResolver struct {
	api    scDiscoverAPI
	nsName string // Cloud Map の名前空間名（例 af.internal）。空 = フォールバック無効
	ttl    time.Duration

	mu    sync.Mutex
	cache map[string]resolvedAgent
}

type resolvedAgent struct {
	addr string
	exp  time.Time
}

// agentDialer は CP→Agent の全経路が共有する。nil のときは素の dial（docker/native の
// ランタイムでは Service Connect が無関係なので、フォールバックは付けない）。
var agentDialer *agentResolver

var agentBaseDialer = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}

// initAgentResolver は ECS 系ランタイムの起動時に一度だけ呼ばれる。DiscoverInstances は
// 名前空間を **名前** で取るのに、こちらが持っているのは ARN なので、起動時に一度だけ
// GetNamespace で名前へ変換する（毎回引くほどのものではない）。名前空間が無い・引けない
// デプロイでは黙ってフォールバック無しに倒す＝従来どおりの挙動。
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

// setAgentResolver は ECS 系ランタイムの起動時に一度だけ呼ばれる。
func setAgentResolver(api scDiscoverAPI, namespaceName string) {
	if api == nil || strings.TrimSpace(namespaceName) == "" {
		return
	}
	agentDialer = &agentResolver{api: api, nsName: namespaceName, ttl: 30 * time.Second, cache: map[string]resolvedAgent{}}
	log.Printf("agent dial: Service Connect の別名が引けないときは Cloud Map(%s) で引き直す", namespaceName)
}

// dialAgent は全クライアント/ダイアラが使う DialContext。
func dialAgent(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := agentBaseDialer.DialContext(ctx, network, addr)
	if err == nil || agentDialer == nil || !isDNSNotFound(err) {
		return c, err
	}
	alt, ok := agentDialer.lookup(ctx, addr)
	if !ok {
		// ★ 元のエラーを返す。フォールバックが空振りしたことで、本来の
		// 「名前が引けない」という診断を隠さない。
		return nil, err
	}
	return agentBaseDialer.DialContext(ctx, network, alt)
}

// isDNSNotFound は「名前が存在しない」だけを拾う。タイムアウトや接続拒否まで拾うと、
// 落ちているだけの Agent に対して Cloud Map を無駄に叩く。
func isDNSNotFound(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de) && de.IsNotFound
}

// lookup は host:port の host を Cloud Map のサービス名として引き、ip:port を返す。
func (r *agentResolver) lookup(ctx context.Context, addr string) (string, bool) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || net.ParseIP(host) != nil {
		return "", false // IP 直指定なら DNS は関係ない＝別の失敗
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
		// 登録されているポートを優先する。無ければ要求されたポートのまま
		// （Service Connect は必ず入れるが、手で登録された実体もありうる）。
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
		// ここに来た＝この CP タスクの /etc/hosts に載っていないサービスを掴んだということ。
		// 運用者が「なぜ CP を再起動すると直るのか」を後から追えるように、必ず 1 行残す。
		log.Printf("agent dial: %s は Service Connect の別名で引けなかった → Cloud Map の %s を使う", host, resolved)
		return resolved, true
	}
	return "", false
}

// agentTransport は CP→Agent の HTTP 全部が共有する Transport。既定の Transport を
// 複製して DialContext だけ差し替える（プロキシ設定・HTTP/2・接続プールの既定は保つ）。
func newAgentTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = dialAgent
	return t
}
