// drawio_preseed.go — fills the stencil cache ahead of time (docs/log/65 §65.5.5 / P1b).
//
//	control-plane drawio-preseed                 # download and store the default bundle
//	control-plane drawio-preseed --all           # all 203 sets (40.8 MB)
//	control-plane drawio-preseed --from <dir>    # from a local stencils/ tree, no network
//	control-plane drawio-preseed --list          # only list what would be stored
//
// An air-gapped deployment uses `--from`: clone drawio once where the network reaches,
// carry `src/main/webapp/stencils` in, and point at that directory. Every file is checked
// against the manifest's sha256, so the contents are guaranteed even when the transport
// that brought them is not. The cache is content-addressed (`<sha256>.xml`), so carrying
// an already-filled cache directory across as a tar does the same job — there is no index
// file.
//
// Why the default is not everything: of the 203 sets and 40.8 MB, `aws4.xml` alone is
// 6.21 MB and `rack/hpe_aruba/switches.xml` 3.67 MB. What an air-gapped admin actually
// needs is cloud/infrastructure diagrams and general drawing, and that fits in 49 sets and
// 17.0 MB; `--all` is there when it does not. Either way, always print what was stored and
// what was not — trimming in silence reads as "everything is in".
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// The default bundle. This is not the same question as trimming the manifest: the manifest
// stays complete (trimming it degrades diagrams silently) and only what is placed in
// advance may be narrowed. A set that is not here is still fetched by the CP on demand
// wherever the network reaches.
var (
	// Whole directories (Azure and Office are many small files).
	drawioPreseedPrefixes = []string{"mscae/", "office/"}
	// Individual sets. `rack/` takes the models but avoids hpe_aruba (3.67 MB).
	drawioPreseedExact = []string{
		// cloud
		"aws4.xml", "aws3.xml", "aws3d.xml", "azure.xml", "gcp2.xml",
		"ibm.xml", "ibm_cloud.xml", "kubernetes.xml", "kubernetes2.xml",
		// network / rack
		"networks.xml", "networks2.xml",
		"rack/apc.xml", "rack/cisco.xml", "rack/dell.xml", "rack/f5.xml",
		"rack/general.xml", "rack/hp.xml", "rack/ibm.xml", "rack/oracle.xml",
		// general drawing
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
	all := fs.Bool("all", false, "seed every entry in the manifest (203 sets / 40.8 MB)")
	from := fs.String("from", "", "read from this directory (drawio's stencils/) instead of from the network")
	dir := fs.String("dir", "", "cache destination (default $WS_DATA/drawio-stencils)")
	list := fs.Bool("list", false, "only list the targets and do nothing")
	_ = fs.Parse(args)

	m, err := loadDrawioManifest()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot read the manifest: %v\n", err)
		os.Exit(1)
	}
	names := drawioPreseedNames(m, *all)
	var total int64
	for _, n := range names {
		total += m.Sets[n].Size
	}
	// Always say what was left out; trimming in silence reads as "everything is in".
	skipped := len(m.Sets) - len(names)
	var skippedBytes int64
	for _, e := range m.Sets {
		skippedBytes += e.Size
	}
	skippedBytes -= total

	fmt.Printf("drawio %s / target %d sets %.1f MB", m.Version, len(names), float64(total)/(1<<20))
	if skipped > 0 {
		fmt.Printf(" (not targeted: %d sets %.1f MB - use --all for everything)", skipped, float64(skippedBytes)/(1<<20))
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
		cacheDir = filepath.Join(envx.Or("WS_DATA", "/tmp/af-data"), "drawio-stencils")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create the cache destination %s: %v\n", cacheDir, err)
		os.Exit(1)
	}
	d := &drawioStencils{cacheDir: cacheDir, loading: map[string]*sync.Mutex{}}
	fmt.Printf("seeding into %s\n", cacheDir)

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
			// Carried in: verify before storing — the manifest guarantees the contents
			// even when the route they arrived by cannot be trusted.
			body, err = os.ReadFile(filepath.Join(*from, filepath.FromSlash(name)))
			if err == nil {
				if verr := verifyDrawioStencil(body, entry); verr != nil {
					err = fmt.Errorf("%s: %w (is the --from directory the one for %s?)", name, verr, m.Version)
				}
			}
			if err == nil {
				// Same directory a running CP serves from, so go through store()
				// rather than writing directly: a half-written file visible under its
				// final name is handed out as if it were whole.
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
	fmt.Printf("seeded %d / already present %d / failed %d\n", done, already, failed)
	if failed > 0 {
		// A missing stencil still opens the diagram (outlines and colours only), so a
		// failure does not abort the run — but the exit status stays honest.
		os.Exit(1)
	}
}
