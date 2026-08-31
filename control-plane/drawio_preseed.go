// drawio_preseed.go — ステンシルのキャッシュを先に埋める（docs/log/65 §65.5.5 / P1b）。
//
//	control-plane drawio-preseed                 # 既定束をダウンロードして投入
//	control-plane drawio-preseed --all           # 203 件すべて（40.8 MB）
//	control-plane drawio-preseed --from <dir>    # ネットワークを使わず、手元の stencils/ から
//	control-plane drawio-preseed --list          # 何が入るかだけ表示（何もしない）
//
// **閉域では `--from` を使う。** 外に出られる場所で drawio を 1 回 clone し、
// `src/main/webapp/stencils` をコピーして持ち込み、そのディレクトリを指す。台帳の
// sha256 で 1 件ずつ照合するので、持ち込みの経路が信用できなくても中身は保証される。
// キャッシュは内容アドレス（`<sha256>.xml`）なので、**投入済みのディレクトリごと tar で
// 運んでも同じことができる**（索引ファイルは無い）。
//
// なぜ既定が全件ではないのか: 203 件 40.8 MB のうち `aws4.xml` だけで 6.21 MB、
// `rack/hpe_aruba/switches.xml` が 3.67 MB を占める。閉域の管理者が本当に要るのは
// クラウド／インフラ図と汎用作図で、そこは 49 件 17.0 MB に収まる。**足りなければ
// `--all` がある**し、どちらの場合も「何を入れて何を入れなかったか」を必ず出す
// （黙って打ち切ると「全部入った」と読まれる）。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// 既定束。**台帳を絞るのとは別の話**である——台帳は全件（絞ると図が黙って劣化する）で、
// 絞ってよいのは「先に置いておく分」だけ。ここに無いセットも、外に出られる環境なら
// 要求された時点で CP が取りに行く。
var (
	// ディレクトリ丸ごと入れるもの（Azure と Office は 1 ファイルが小さく数が多い）。
	drawioPreseedPrefixes = []string{"mscae/", "office/"}
	// 単体で指定するもの。`rack/` は hpe_aruba（3.67 MB）を避けて機種だけ拾う。
	drawioPreseedExact = []string{
		// クラウド
		"aws4.xml", "aws3.xml", "aws3d.xml", "azure.xml", "gcp2.xml",
		"ibm.xml", "ibm_cloud.xml", "kubernetes.xml", "kubernetes2.xml",
		// ネットワーク／ラック
		"networks.xml", "networks2.xml",
		"rack/apc.xml", "rack/cisco.xml", "rack/dell.xml", "rack/f5.xml",
		"rack/general.xml", "rack/hp.xml", "rack/ibm.xml", "rack/oracle.xml",
		// 汎用作図
		"basic.xml", "flowchart.xml", "bpmn.xml", "eip.xml",
		"cabinets.xml", "floorplan.xml", "atlassian.xml", "salesforce.xml",
		"veeam/veeam.xml",
	}
)

func drawioPreseedNames(m drawioStencilManifest, all bool) []string {
	var names []string
	if all {
		for name := range m.Sets {
			names = append(names, name)
		}
		sort.Strings(names)
		return names
	}
	seen := map[string]bool{}
	for _, n := range drawioPreseedExact {
		if _, ok := m.Sets[n]; ok && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for name := range m.Sets {
		for _, p := range drawioPreseedPrefixes {
			if strings.HasPrefix(name, p) && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

func runDrawioPreseed(args []string) {
	fs := flag.NewFlagSet("drawio-preseed", flag.ExitOnError)
	all := fs.Bool("all", false, "台帳の全件（203 件 / 40.8 MB）を投入する")
	from := fs.String("from", "", "ネットワークではなく、このディレクトリ（drawio の stencils/）から読む")
	dir := fs.String("dir", "", "キャッシュ先（既定 $WS_DATA/drawio-stencils）")
	list := fs.Bool("list", false, "対象を並べるだけで何もしない")
	_ = fs.Parse(args)

	m, err := loadDrawioManifest()
	if err != nil {
		fmt.Fprintf(os.Stderr, "台帳を読めない: %v\n", err)
		os.Exit(1)
	}
	names := drawioPreseedNames(m, *all)
	var total int64
	for _, n := range names {
		total += m.Sets[n].Size
	}
	// **入らなかったものを必ず言う。** 黙って絞ると「全部入った」と読まれる。
	skipped := len(m.Sets) - len(names)
	var skippedBytes int64
	for _, e := range m.Sets {
		skippedBytes += e.Size
	}
	skippedBytes -= total

	fmt.Printf("drawio %s / 対象 %d 件 %.1f MB", m.Version, len(names), float64(total)/(1<<20))
	if skipped > 0 {
		fmt.Printf("（対象外 %d 件 %.1f MB —— 全件なら --all）", skipped, float64(skippedBytes)/(1<<20))
	}
	fmt.Println()
	if *list {
		for _, n := range names {
			fmt.Printf("  %-44s %7.2f MB\n", n, float64(m.Sets[n].Size)/(1<<20))
		}
		return
	}

	cacheDir := *dir
	if cacheDir == "" {
		cacheDir = filepath.Join(envOr("WS_DATA", "/tmp/af-data"), "drawio-stencils")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "キャッシュ先を作れない %s: %v\n", cacheDir, err)
		os.Exit(1)
	}
	d := &drawioStencils{cacheDir: cacheDir, loading: map[string]*sync.Mutex{}}
	fmt.Printf("投入先 %s\n", cacheDir)

	var done, already, failed int
	for _, name := range names {
		entry := m.Sets[name]
		path := d.pathFor(entry)
		if b, err := os.ReadFile(path); err == nil && verifyDrawioStencil(b, entry) == nil {
			already++
			continue
		}
		var body []byte
		if *from != "" {
			// 持ち込み。**照合してから置く** —— 経路が信用できなくても中身は台帳が保証する。
			body, err = os.ReadFile(filepath.Join(*from, filepath.FromSlash(name)))
			if err == nil {
				if verr := verifyDrawioStencil(body, entry); verr != nil {
					err = fmt.Errorf("%s: %w（--from のディレクトリは %s のものか）", name, verr, m.Version)
				}
			}
			if err == nil {
				// 稼働中の CP と同じディレクトリなので、直に書かず store() を通す
				// （書きかけが正規名で見えると壊れたものが配られる）。
				err = d.store(path, body)
			}
		} else {
			_, err = d.fetch(context.Background(), m, name, entry)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "  × %s: %v\n", name, err)
			failed++
			continue
		}
		done++
		if (done+already)%20 == 0 {
			fmt.Printf("  %d / %d …\n", done+already, len(names))
		}
	}
	fmt.Printf("投入 %d 件 / 既にあった %d 件 / 失敗 %d 件\n", done, already, failed)
	if failed > 0 {
		// 失敗しても図は開く（枠と色だけになる）。運用は続けられるので落とさないが、
		// 終了ステータスは正直に返す。
		os.Exit(1)
	}
}
