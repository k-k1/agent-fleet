package main

// install_pg_client.go — workspace-agent install-pg-client [<major>]
//
// Installs postgresql-client-<major>, libpq5, and postgresql-client-common from
// the Debian trixie Packages index without root. Package version and SHA256 are
// read from the index at install time — no pinned filenames.
//
// A pure-Go ar reader extracts data.tar.* from each .deb; tar then unpacks it
// into ~/.local/share/agent-fleet/pg-client/. Wrappers with LD_LIBRARY_PATH are
// written to ~/.local/bin/{psql,pg_dump,pg_restore}.
//
// Exit codes: 0 ok, 1 error, 2 usage.

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func runInstallPgClient(args []string) {
	major := pgDefaultMajor
	if len(args) > 0 && args[0] != "" && !strings.HasPrefix(args[0], "-") {
		major = args[0]
	}
	switch major {
	case "16", "17", "18":
	default:
		fmt.Fprintln(os.Stderr, "usage: workspace-agent install-pg-client [16|17|18]")
		os.Exit(2)
	}
	if err := installPgClient(major); err != nil {
		fmt.Fprintf(os.Stderr, "[install-pg-client] %v\n", err)
		os.Exit(1)
	}
}

// pgClientRoot is where the merged extracted .deb trees land.
func pgClientRoot() string {
	return filepath.Join(agentFleetShareDir(), "pg-client")
}

// debArch returns the Debian architecture string for this container.
func debArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	default:
		return runtime.GOARCH
	}
}

// debLibTriplet returns the GNU triplet for the shared-library directory.
func debLibTriplet() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64-linux-gnu"
	case "arm64":
		return "aarch64-linux-gnu"
	default:
		return runtime.GOARCH + "-linux-gnu"
	}
}

// debPkg holds the fields from a Packages stanza that we need.
type debPkg struct {
	Name     string
	Version  string
	Filename string
	SHA256   string
}

func installPgClient(major string) error {
	psqlWrapper := filepath.Join(homeDir(), ".local", "bin", "psql")
	if fileExecutable(psqlWrapper) {
		fmt.Fprintf(os.Stderr, "[install-pg-client] pg-client %s already installed\n", major)
		return nil
	}

	arch := debArch()
	pkgsURL := fmt.Sprintf(
		"https://deb.debian.org/debian/dists/trixie/main/binary-%s/Packages.gz", arch,
	)
	fmt.Fprintf(os.Stderr, "[install-pg-client] fetching %s ...\n", pkgsURL)
	index, err := fetchDebPackages(pkgsURL)
	if err != nil {
		return err
	}

	wanted := []string{
		"postgresql-client-" + major,
		"libpq5",
		"postgresql-client-common",
	}
	pkgs := make([]debPkg, 0, len(wanted))
	for _, name := range wanted {
		p, ok := index[name]
		if !ok {
			return fmt.Errorf("package %q not found in Packages index", name)
		}
		pkgs = append(pkgs, p)
	}

	shareDir := agentFleetShareDir()
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(shareDir, ".pg-client-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	extractRoot := filepath.Join(staging, "root")
	if err := os.MkdirAll(extractRoot, 0o755); err != nil {
		return err
	}

	const debBase = "https://deb.debian.org/debian/"
	for _, pkg := range pkgs {
		url := debBase + pkg.Filename
		debPath := filepath.Join(staging, filepath.Base(pkg.Filename))
		fmt.Fprintf(os.Stderr, "[install-pg-client] downloading %s (%s) ...\n", pkg.Name, pkg.Version)
		if err := runCmd("curl", "-fsSL", "--retry", "3", "--retry-delay", "2",
			"--retry-connrefused", "-o", debPath, url); err != nil {
			return fmt.Errorf("download %s: %w", url, err)
		}
		got, err := fileSHA256(debPath)
		if err != nil {
			return err
		}
		if got != pkg.SHA256 {
			return fmt.Errorf("sha256 mismatch for %s\n  got:  %s\n  want: %s",
				url, got, pkg.SHA256)
		}
		if err := extractDeb(debPath, extractRoot); err != nil {
			return fmt.Errorf("extract %s: %w", pkg.Name, err)
		}
	}

	// Create libpq.so.5 → libpq.so.5.N symlink (normally done by ldconfig).
	libDir := filepath.Join(extractRoot, "usr", "lib", debLibTriplet())
	makeLibpqSymlink(libDir)

	// Atomic place.
	dest := pgClientRoot()
	_ = os.RemoveAll(dest)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.Rename(extractRoot, dest); err != nil {
		return err
	}

	binDir := filepath.Join(homeDir(), ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	for _, bin := range []string{"psql", "pg_dump", "pg_restore"} {
		if err := writePgWrapper(binDir, dest, major, bin); err != nil {
			return err
		}
	}

	out, err := exec.Command(psqlWrapper, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("psql --version: %w\n%s", err, string(out))
	}
	fmt.Fprintf(os.Stderr, "[install-pg-client] installed: %s\n", strings.TrimSpace(string(out)))
	return nil
}

// fetchDebPackages downloads and parses a Packages.gz index.
func fetchDebPackages(url string) (map[string]debPkg, error) {
	cl := &http.Client{Timeout: 60 * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}
	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gunzip %s: %w", url, err)
	}
	defer gr.Close()
	b, err := io.ReadAll(io.LimitReader(gr, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	return parseDebPackages(string(b)), nil
}

// parseDebPackages parses a dpkg-style Packages file into a name → debPkg map.
func parseDebPackages(text string) map[string]debPkg {
	out := map[string]debPkg{}
	for _, stanza := range strings.Split(text, "\n\n") {
		var p debPkg
		for _, line := range strings.Split(stanza, "\n") {
			k, v, ok := strings.Cut(line, ": ")
			if !ok {
				continue
			}
			switch k {
			case "Package":
				p.Name = v
			case "Version":
				p.Version = v
			case "Filename":
				p.Filename = v
			case "SHA256":
				p.SHA256 = v
			}
		}
		if p.Name != "" && p.Filename != "" {
			out[p.Name] = p
		}
	}
	return out
}

// extractDeb extracts the data.tar.* payload from a .deb file into destDir.
// Uses a pure-Go ar reader so no external ar binary is required.
func extractDeb(debPath, destDir string) error {
	f, err := os.Open(debPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// Verify ar magic header.
	magic := make([]byte, 8)
	if _, err := io.ReadFull(f, magic); err != nil {
		return fmt.Errorf("read ar magic: %w", err)
	}
	if string(magic) != "!<arch>\n" {
		return fmt.Errorf("%s: not an ar archive", debPath)
	}

	// Walk members looking for data.tar.*.
	for {
		var hdr [60]byte
		if _, err := io.ReadFull(f, hdr[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return fmt.Errorf("read ar header: %w", err)
		}
		name := strings.TrimRight(string(hdr[0:16]), " /")
		var size int64
		fmt.Sscanf(strings.TrimSpace(string(hdr[48:58])), "%d", &size)

		if strings.HasPrefix(name, "data.tar") {
			tmp := filepath.Join(filepath.Dir(debPath), name)
			if err := writeArMember(f, tmp, size); err != nil {
				return err
			}
			if err := runCmd("tar", "-xf", tmp, "-C", destDir); err != nil {
				_ = os.Remove(tmp)
				return fmt.Errorf("tar -xf %s: %w", name, err)
			}
			_ = os.Remove(tmp)
			return nil
		}

		// Skip over this member (ar pads to even byte boundary).
		skip := size
		if size%2 != 0 {
			skip++
		}
		if _, err := io.CopyN(io.Discard, f, skip); err != nil {
			return fmt.Errorf("skip ar member %q: %w", name, err)
		}
	}
	return fmt.Errorf("%s: no data.tar.* member", debPath)
}

// writeArMember copies exactly size bytes from r into a new file at path, then
// consumes the padding byte if size is odd.
func writeArMember(r io.Reader, path string, size int64) error {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(out, r, size); err != nil {
		out.Close()
		_ = os.Remove(path)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	if size%2 != 0 {
		var pad [1]byte
		_, _ = r.Read(pad[:])
	}
	return nil
}

// makeLibpqSymlink creates libpq.so.5 → libpq.so.5.N in libDir if not present.
func makeLibpqSymlink(libDir string) {
	entries, err := os.ReadDir(libDir)
	if err != nil {
		return
	}
	soname := "libpq.so.5"
	if _, err := os.Lstat(filepath.Join(libDir, soname)); err == nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), soname+".") {
			_ = os.Symlink(e.Name(), filepath.Join(libDir, soname))
			return
		}
	}
}

// writePgWrapper writes a wrapper script for one pg binary to binDir.
func writePgWrapper(binDir, clientRoot, major, bin string) error {
	libDir := filepath.Join(clientRoot, "usr", "lib", debLibTriplet())
	binPath := filepath.Join(clientRoot, "usr", "lib", "postgresql", major, "bin", bin)
	content := fmt.Sprintf("#!/bin/sh\nexec env LD_LIBRARY_PATH=%q\"${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}\" %q \"$@\"\n",
		libDir, binPath)
	dest := filepath.Join(binDir, bin)
	tmp := dest + ".part"
	if err := os.WriteFile(tmp, []byte(content), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}
