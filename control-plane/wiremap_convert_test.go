// wiremap_convert_test.go — 「map → struct へ変換した 1 サイトが、ワイヤを 1 バイトも
// 変えていない」ことの証明（CONTRACT-MAP / 脚③）。
//
// 🔴 **旧 map リテラルはここに写して残す。** 変換したあと、production 側にはもう
// 「元の形」がどこにも無い。**基準が消えるので、消さずにテストへ移す。**
// 写しは production から機械的にコピーしたもので、**書き換えない**
// （書き換えた瞬間、基準ではなく「新しい実装の別表現」になる）。
//
// ハーネス本体と罠の対照は wiremap_equiv_test.go。
// ここは「どのサイトを変換し、その等価をどう示したか」だけを持つ。
package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/wiretest"
)

// wiremapConvertedMarker — 変換で生まれた型の doc コメントに必ず書く印。
//
// 🔴 型名の接尾辞（`…Wire`）では判別できない。**`sessionWire` のように
// CONTRACT-MAP より前から在る型が同じ綴りを持つ**ので、名前で拾うと
// 「証明が要る型」と「元から struct だった型」が混ざる。
// **「その型が map を置き換えたものか」は名前ではなく由来の情報**なので、
// 由来をコメントに書き、それを機械が読む。
const wiremapConvertedMarker = "旧: map[string]any"

// wiremapConvertedWireTypes は「map を置き換えた」と doc コメントで宣言している
// struct 型の名前を返す。
func wiremapConvertedWireTypes(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, perr := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if perr != nil {
			return perr
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE || gd.Doc == nil {
				continue
			}
			if !strings.Contains(gd.Doc.Text(), wiremapConvertedMarker) {
				continue
			}
			for _, sp := range gd.Specs {
				ts, ok := sp.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if _, ok := ts.Type.(*ast.StructType); ok {
					out = append(out, ts.Name.Name)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("走査に失敗: %v", err)
	}
	sort.Strings(out)
	return out
}

// --- ① egressAPI.checkHosts（Console: EgressCheck）---

type egressCheckIn struct {
	Configured bool
	Mode       string
	Enforce    bool
	Hosts      map[string]egressHostVerdict
}

func TestWireEquivEgressCheck(t *testing.T) {
	inputs := []egressCheckIn{
		{Configured: true, Mode: "enforce", Enforce: true,
			Hosts: map[string]egressHostVerdict{"a.example": {Host: "a.example", Allowed: true, Proposed: false}}},
		// 呼び出し側は make() 済みなので nil にはならないが、**空 map と nil map は
		// `{}` と `null` で別物**なので両方測る。
		{Configured: false, Mode: "log-only", Enforce: false, Hosts: map[string]egressHostVerdict{}},
	}
	got := wiretest.AssertEquiv(t, "egressAPI.checkHosts", inputs,
		func(in egressCheckIn) any { // 旧（egress_member.go の map リテラルの写し）
			return map[string]any{
				"configured": in.Configured, "mode": in.Mode, "enforce": in.Enforce, "hosts": in.Hosts,
			}
		},
		func(in egressCheckIn) any {
			return egressCheckWire{
				Configured: in.Configured, Mode: in.Mode, Enforce: in.Enforce, Hosts: in.Hosts,
			}
		})
	t.Logf("突き合わせ方式: %s", got)
}

// --- ② adminAPI.hostStats（Console: HostStats）---

type hostStatsIn struct {
	Load1    float64
	Ncpu     int
	MemUsed  uint64
	MemTotal uint64
}

func TestWireEquivHostStats(t *testing.T) {
	inputs := []hostStatsIn{
		{Load1: 1.25, Ncpu: 8, MemUsed: 3 << 30, MemTotal: 10 << 30},
		// 🔴 uint64 を float64 で受け直していないことを実際に測る標本。
		// 2^53 を超える値は float64 では正確に表せない。
		{Load1: 0, Ncpu: 1, MemUsed: 1<<53 + 1, MemTotal: 1<<62 + 3},
	}
	got := wiretest.AssertEquiv(t, "adminAPI.hostStats", inputs,
		func(in hostStatsIn) any { // 旧（metrics.go の map リテラルの写し）
			return map[string]any{
				"load1": in.Load1, "ncpu": in.Ncpu, "mem_used": in.MemUsed, "mem_total": in.MemTotal,
			}
		},
		func(in hostStatsIn) any {
			return hostStatsWire{
				Load1: in.Load1, Ncpu: in.Ncpu, MemUsed: in.MemUsed, MemTotal: in.MemTotal,
			}
		})
	t.Logf("突き合わせ方式: %s", got)
}

// --- ③ updateStatus（Console: HostUpdateStatus）---

type updateStatusIn struct {
	Current   string
	Installed string
	Systemd   bool
}

func TestWireEquivUpdateStatus(t *testing.T) {
	inputs := []updateStatusIn{
		{Current: "v1", Installed: "v2", Systemd: true},
		// installed="" が「staged 無し」の表現。**キーは出続けなければならない。**
		{Current: "v1", Installed: "", Systemd: false},
		{Current: "v1", Installed: "v1", Systemd: false}, // 同版＝restartRequired false
	}
	got := wiretest.AssertEquiv(t, "updateStatus", inputs,
		func(in updateStatusIn) any { // 旧（update.go の map リテラルの写し）
			return map[string]any{
				"current":         in.Current,
				"installed":       in.Installed,
				"restartRequired": in.Installed != "" && in.Installed != in.Current,
				"systemd":         in.Systemd,
			}
		},
		func(in updateStatusIn) any {
			return hostUpdateStatusWire{
				Current:         in.Current,
				Installed:       in.Installed,
				RestartRequired: in.Installed != "" && in.Installed != in.Current,
				Systemd:         in.Systemd,
			}
		})
	t.Logf("突き合わせ方式: %s", got)
}

// --- ④ workItemsAPI.workItemsPayload（Console: WorkItemPayload）---
//
// 形状関数なので、これ 1 つで 3 サイト（list ×1 / refresh ×2）が型を得る。

type workItemsPayloadIn struct {
	Items     []workItemDTO
	Queries   []workItemQueryDTO
	Sessions  []workItemSessionDTO
	FetchedAt string
	Running   bool
}

func TestWireEquivWorkItemsPayload(t *testing.T) {
	inputs := []workItemsPayloadIn{
		{
			Items:     []workItemDTO{{ID: "i1", Labels: []string{"bug"}}},
			Queries:   []workItemQueryDTO{{ID: "q1", Enabled: true}},
			Sessions:  []workItemSessionDTO{{ID: "s1"}},
			FetchedAt: "2026-09-03T00:00:00Z", Running: true,
		},
		// 🔴 production は make(…, 0, n) 済みなので**空スライス**が出る。
		// nil スライスは `null`・空スライスは `[]` で**別物**なので、両方を測る
		//（ゼロ値ケース＝全部 nil はハーネスが自動で足す）。
		{
			Items:    []workItemDTO{},
			Queries:  []workItemQueryDTO{},
			Sessions: []workItemSessionDTO{},
		},
	}
	got := wiretest.AssertEquiv(t, "workItemsAPI.workItemsPayload", inputs,
		func(in workItemsPayloadIn) any { // 旧（workitems.go の map リテラルの写し）
			return map[string]any{
				"items": in.Items, "queries": in.Queries, "sessions": in.Sessions,
				"fetchedAt": in.FetchedAt, "running": in.Running,
			}
		},
		func(in workItemsPayloadIn) any {
			return workItemsPayloadWire{
				Items: in.Items, Queries: in.Queries, Sessions: in.Sessions,
				FetchedAt: in.FetchedAt, Running: in.Running,
			}
		})
	t.Logf("突き合わせ方式: %s", got)
}

// --- ⑤ gitServerAPI.blob（Console: Blob）---
//
// 4 つの出口がそれぞれ違うキー集合を返す。**出口ごとに 1 ケース**置く。

type gitBlobIn struct {
	Ref, Path string
	Size      int64
	TooLarge  bool
	LFS       bool
	LFSOID    string
	Binary    bool
	Content   *string // nil = キー無し / &"" = 空ファイル（キーは出る）
}

func TestWireEquivGitBlob(t *testing.T) {
	empty, text := "", "hello"
	inputs := []gitBlobIn{
		{Ref: "main", Path: "a.txt", Size: 5, Content: &text},
		{Ref: "main", Path: "big.bin", Size: 1 << 30, TooLarge: true},
		{Ref: "main", Path: "x.psd", Size: 12, LFS: true,
			LFSOID: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{Ref: "main", Path: "y.bin", Size: 3, Binary: true},
		// 🔴 空ファイル。旧は `"content": ""` を**出す**。string+omitempty なら消えるので、
		// ここが赤くならないなら Content をポインタにした意味が無い。
		{Ref: "main", Path: "empty.txt", Size: 0, Content: &empty},
	}
	got := wiretest.AssertEquiv(t, "gitServerAPI.blob", inputs,
		func(in gitBlobIn) any { // 旧（internal_git_browse.go の resp の写し）
			resp := map[string]any{"ref": in.Ref, "path": in.Path, "size": in.Size}
			if in.TooLarge {
				resp["too_large"] = true
			}
			if in.LFS {
				resp["lfs"] = true
				if in.LFSOID != "" {
					resp["lfs_oid"] = in.LFSOID
				}
			}
			if in.Binary {
				resp["binary"] = true
			}
			if in.Content != nil {
				resp["content"] = *in.Content
			}
			return resp
		},
		func(in gitBlobIn) any {
			return gitBlobWire{Ref: in.Ref, Path: in.Path, Size: in.Size,
				TooLarge: in.TooLarge, LFS: in.LFS, LFSOID: in.LFSOID,
				Binary: in.Binary, Content: in.Content}
		})
	t.Logf("突き合わせ方式: %s", got)
}

// --- ⑥ gitServerAPI.repoDTO（Console: InternalRepo）---

type internalRepoIn struct{ Name, DefaultBranch, CloneURL, CreatedAt string }

func TestWireEquivInternalRepo(t *testing.T) {
	inputs := []internalRepoIn{
		{Name: "web", DefaultBranch: "main", CloneURL: "https://x/git/web", CreatedAt: "2026-09-03T00:00:00Z"},
		{Name: "bare", DefaultBranch: "", CloneURL: "", CreatedAt: ""}, // 空文字でもキーは出る
	}
	got := wiretest.AssertEquiv(t, "gitServerAPI.repoDTO", inputs,
		func(in internalRepoIn) any { // 旧（internal_git.go の map リテラルの写し）
			return map[string]any{
				"name": in.Name, "default_branch": in.DefaultBranch,
				"clone_url": in.CloneURL, "created_at": in.CreatedAt, "provider": "internal",
			}
		},
		func(in internalRepoIn) any {
			return internalRepoWire{Name: in.Name, DefaultBranch: in.DefaultBranch,
				CloneURL: in.CloneURL, CreatedAt: in.CreatedAt, Provider: "internal"}
		})
	t.Logf("突き合わせ方式: %s", got)
}

// --- ⑦ sessionHandoffAPI の 2 変種（Console: HandoffOffer）---
//
// 🔴 **同じ TS 型 `HandoffOffer` を 2 つの窓口が分担して埋めている**（create=13 / listReceived=16）。
// **両方**を測らないと、狭いほうだけ守って広いほうを落とす。

type handoffIn struct {
	ID, SessionID, SessionName, RecipientUserKey, Title, Status  string
	Branch, RepoRemote, HeadSha, CreatedAt, ExpiresAt, DecidedAt string
	AcceptedSessionName                                          string
	OwnerUserKey, Prompt, SourceSessionKind                      string
}

func handoffOldBase(in handoffIn) map[string]any { // 旧 handoffOfferDTO の写し
	return map[string]any{
		"id": in.ID, "sessionId": in.SessionID, "sessionName": in.SessionName,
		"recipientUserKey": in.RecipientUserKey, "title": in.Title, "status": in.Status,
		"branch": in.Branch, "repoRemote": in.RepoRemote, "headSha": in.HeadSha,
		"createdAt": in.CreatedAt, "expiresAt": in.ExpiresAt, "decidedAt": in.DecidedAt,
		"acceptedSessionName": in.AcceptedSessionName,
	}
}

func handoffNewBase(in handoffIn) handoffOfferWire {
	return handoffOfferWire{
		ID: in.ID, SessionID: in.SessionID, SessionName: in.SessionName,
		RecipientUserKey: in.RecipientUserKey, Title: in.Title, Status: in.Status,
		Branch: in.Branch, RepoRemote: in.RepoRemote, HeadSha: in.HeadSha,
		CreatedAt: in.CreatedAt, ExpiresAt: in.ExpiresAt, DecidedAt: in.DecidedAt,
		AcceptedSessionName: in.AcceptedSessionName,
	}
}

func handoffInputs() []handoffIn {
	return []handoffIn{
		{ID: "o1", SessionID: "c1", SessionName: "s1", RecipientUserKey: "u2", Title: "t",
			Status: "pending", Branch: "b", RepoRemote: "r", HeadSha: "h",
			CreatedAt: "c", ExpiresAt: "e", DecidedAt: "d", AcceptedSessionName: "a",
			OwnerUserKey: "u1", Prompt: "本文", SourceSessionKind: "claude"},
		// 🔴 **omitempty を使わなかった理由そのもの**: keys[…] にキーが無ければ
		// ownerUserKey は ""、a.open() は空を返しうる。旧はそれを**キーごと出す**。
		{ID: "o2", Status: "withdrawn", OwnerUserKey: "", Prompt: "", SourceSessionKind: ""},
	}
}

func TestWireEquivHandoffOfferCreate(t *testing.T) {
	got := wiretest.AssertEquiv(t, "sessionHandoffAPI.create", handoffInputs(),
		func(in handoffIn) any { return handoffOldBase(in) },
		func(in handoffIn) any { return handoffNewBase(in) })
	t.Logf("突き合わせ方式: %s", got)
}

func TestWireEquivHandoffOfferInbox(t *testing.T) {
	got := wiretest.AssertEquiv(t, "sessionHandoffAPI.listReceived", handoffInputs(),
		func(in handoffIn) any { // 旧: DTO に 3 キーを後から足していた形の写し
			d := handoffOldBase(in)
			d["ownerUserKey"] = in.OwnerUserKey
			d["prompt"] = in.Prompt
			d["sourceSessionKind"] = in.SourceSessionKind
			return d
		},
		func(in handoffIn) any {
			return handoffOfferInboxWire{
				handoffOfferWire:  handoffNewBase(in),
				OwnerUserKey:      in.OwnerUserKey,
				Prompt:            in.Prompt,
				SourceSessionKind: in.SourceSessionKind,
			}
		})
	t.Logf("突き合わせ方式: %s", got)
}

// TestWireHandoffKeyCountsAreIndependentlyFixed — **絶対数を独立に固定する。**
//
// 🔴 等価ハーネスは「旧の写しと新が同じ」しか見ない。**写しそのものを取り違えていたら、
// 両方が同じだけ間違ったまま緑になる。**（今日のウェーブで 3 度出た形の最後の 1 つ:
// 「独立に測ったつもりの 2 つが一致したときこそ系統誤差を疑う」。）
// そこで**出力キーの個数を写しに依存しない絶対値として固定する**。
// 埋め込みで実効 json 名が衝突すると encoding/json は**どちらも出さない**ので、
// その事故もここで数が減って捕まる。
func TestWireHandoffKeyCountsAreIndependentlyFixed(t *testing.T) {
	in := handoffInputs()[0]
	for _, tc := range []struct {
		name string
		v    any
		want int
	}{
		{"create（基本形）", handoffNewBase(in), 13},
		{"listReceived（受信箱）", handoffOfferInboxWire{
			handoffOfferWire:  handoffNewBase(in),
			OwnerUserKey:      in.OwnerUserKey,
			Prompt:            in.Prompt,
			SourceSessionKind: in.SourceSessionKind,
		}, 16},
	} {
		b, err := json.Marshal(tc.v)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(m) != tc.want {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			t.Errorf("%s のキー数 = %d, want %d\n  実際: %v\n"+
				"  減っているなら埋め込みで実効 json 名が衝突している疑い"+
				"（encoding/json は同深さで同名ならどちらも出さない）。", tc.name, len(m), tc.want, keys)
		}
	}
}

// TestWireEquivConvertedSitesAreAllCovered — この PR で変換した形状に
// 等価テストが 1 本ずつ在ることを機械で見る。
//
// 🔴 **なぜ要るか**: 変換だけして等価テストを書き忘れても、**全ゲートは緑のまま通る**
// （型検査は通り、ゴールデンからはそのサイトが消えるだけ）。
// 「証明が付いていない変換」を捕まえる網が他に無い。
func TestWireEquivConvertedSitesAreAllCovered(t *testing.T) {
	// 変換した wire 型 → 等価テストの名前。**型を足したらここも足す。**
	covered := map[string]string{
		"egressCheckWire":       "TestWireEquivEgressCheck",
		"hostStatsWire":         "TestWireEquivHostStats",
		"hostUpdateStatusWire":  "TestWireEquivUpdateStatus",
		"workItemsPayloadWire":  "TestWireEquivWorkItemsPayload",
		"gitBlobWire":           "TestWireEquivGitBlob",
		"internalRepoWire":      "TestWireEquivInternalRepo",
		"handoffOfferWire":      "TestWireEquivHandoffOfferCreate",
		"handoffOfferInboxWire": "TestWireEquivHandoffOfferInbox",
		// internal/tenantsrv に在る型。wire 型が非公開なので**証明はそのパッケージの中**
		// （control-plane/internal/tenantsrv/wiremap_convert_test.go）。
		"tenantNetworkWire":      "TestWireEquivTenantNetwork（internal/tenantsrv）",
		"tenantNetworkSavedWire": "TestWireEquivTenantNetworkSaved（internal/tenantsrv）",
		"tenantSlotClassWire":    "TestWireEquivTenantSlotClass（internal/tenantsrv）",
		"tenantLoginWire":        "TestWireEquivTenantLogin（internal/tenantsrv）",
	}
	declared := wiremapConvertedWireTypes(t, ".")
	for _, name := range declared {
		if _, ok := covered[name]; !ok {
			t.Errorf("%s は CONTRACT-MAP が足した wire 型だが、等価テストが登録されていない。"+
				"変換だけして証明を書き忘れると全ゲート緑のまま通るので、ここで止める。", name)
		}
	}
	for name := range covered {
		found := false
		for _, d := range declared {
			if d == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s の等価テストが登録されているが、型がソースに無い（消したなら表からも消すこと）", name)
		}
	}
	if len(declared) == 0 {
		t.Fatal("wire 型を 1 つも見つけられなかった（走査が壊れている）")
	}
	t.Logf("変換済みの wire 型: %d 個", len(declared))
}
