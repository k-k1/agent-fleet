package cloudruns

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"strings"
	"sync"
)

// gcloudCommandsGz is the pinned Google Cloud SDK's command table, one line per leaf command
// path ("compute instances list"). gen_gcloud_commands.py writes it; its first line names the
// SDK version it was read from.
//
//go:embed gcloud_commands.txt.gz
var gcloudCommandsGz []byte

// gcloudCommands holds every known path and every prefix of one (a command group is a command
// word sequence too), keyed by the words joined with a single space.
var gcloudCommands = sync.OnceValues(func() (map[string]bool, string) {
	zr, err := gzip.NewReader(bytes.NewReader(gcloudCommandsGz))
	if err != nil {
		return nil, ""
	}
	defer zr.Close()
	out, version := map[string]bool{}, ""
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "# gcloud "); ok {
			version = v
			continue
		}
		f := strings.Fields(line)
		for i := range f {
			out[strings.Join(f[:i+1], " ")] = true
		}
	}
	return out, version
})

// gcloudCommandPath returns the longest command path of the pinned SDK that the words start
// with, nil when the first word is not a known command. A word that is not part of a longer
// known path (a flag, a resource name, a secret) ends the match and is never kept.
func gcloudCommandPath(words []string) []string {
	known, _ := gcloudCommands()
	n := 0
	for n < len(words) && known[strings.Join(words[:n+1], " ")] {
		n++
	}
	return words[:n]
}
