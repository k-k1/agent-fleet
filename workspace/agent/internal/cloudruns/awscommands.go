package cloudruns

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"strings"
	"sync"
)

// awsCommandsGz is the pinned aws-cli's command table, one line per service: the service
// command followed by its operation commands. gen_aws_commands.py writes it; its first line
// names the CLI version it was read from.
//
//go:embed aws_commands.txt.gz
var awsCommandsGz []byte

var awsCommands = sync.OnceValues(func() (map[string]map[string]bool, string) {
	zr, err := gzip.NewReader(bytes.NewReader(awsCommandsGz))
	if err != nil {
		return nil, ""
	}
	defer zr.Close()
	out, version := map[string]map[string]bool{}, ""
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "# aws-cli "); ok {
			version = v
			continue
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		ops := make(map[string]bool, len(f)-1)
		for _, op := range f[1:] {
			ops[op] = true
		}
		out[f[0]] = ops
	}
	return out, version
})

// knownAWSCommand reports whether `aws service operation` is a command of the pinned CLI.
func knownAWSCommand(service, operation string) bool {
	m, _ := awsCommands()
	return m[service][operation]
}
