package main

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// AllowList exempts known-good hits. Paths are not secret, so this file is
// plain text: each line is `<term-id or *> <path-glob>`, where `*` matches any
// run of characters including `/` and `!`.
//
// It exists for one case only: a forbidden term that is also an ordinary English
// word turning up in a third-party file baked into the images. Exempt the
// narrowest path that works, and say in a comment what was inspected.
type AllowList struct {
	rules []allowRule
}

type allowRule struct {
	id string
	re *regexp.Regexp
}

func ParseAllow(r io.Reader) (*AllowList, error) {
	a := &AllowList{}
	sc := bufio.NewScanner(r)
	ln := 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 {
			return nil, fmt.Errorf("line %d: want `<term-id|*> <path-glob>`", ln)
		}
		var b strings.Builder
		b.WriteString("^")
		for i, part := range strings.Split(f[1], "*") {
			if i > 0 {
				b.WriteString(".*")
			}
			b.WriteString(regexp.QuoteMeta(part))
		}
		b.WriteString("$")
		re, err := regexp.Compile(b.String())
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", ln, err)
		}
		a.rules = append(a.rules, allowRule{id: f[0], re: re})
	}
	return a, sc.Err()
}

func (a *AllowList) Allows(id, path string) bool {
	if a == nil {
		return false
	}
	for _, r := range a.rules {
		if (r.id == "*" || r.id == id) && r.re.MatchString(path) {
			return true
		}
	}
	return false
}
