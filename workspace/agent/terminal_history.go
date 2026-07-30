package main

// Terminal history is a deliberately short-lived, workspace-local ring buffer.
// tmux feeds it independently of browser attachments, so a reload or a session
// exit does not erase the screen the operator was looking at. It is not an audit
// log: the default directory is under /tmp and disappears with the container.

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const terminalHistoryMaxBytes int64 = 4 << 20

func terminalHistoryDir() string {
	if v := os.Getenv("AF_TERMINAL_HISTORY_DIR"); v != "" {
		return v
	}
	if terminalHistoryRetention() > 0 {
		return filepath.Join(paths.HomeDir(), ".local", "share", "agent-fleet", "terminal-history")
	}
	return ephemeralTerminalHistoryDir()
}

func ephemeralTerminalHistoryDir() string {
	return filepath.Join(os.TempDir(), "agent-fleet-terminal-history-"+strconv.Itoa(os.Getuid()))
}

func persistentTerminalHistoryDir() string {
	return filepath.Join(paths.HomeDir(), ".local", "share", "agent-fleet", "terminal-history")
}

func terminalHistoryRetention() time.Duration {
	days, _ := strconv.Atoi(os.Getenv("AF_TERMINAL_HISTORY_RETENTION_DAYS"))
	if days <= 0 {
		return 0
	}
	return time.Duration(days) * 24 * time.Hour
}

func startTerminalHistoryJanitor() {
	cleanupTerminalHistory()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			cleanupTerminalHistory()
		}
	}()
}

func cleanupTerminalHistory() {
	retention := terminalHistoryRetention()
	dir := persistentTerminalHistoryDir()
	if retention == 0 {
		// Turning tenant persistence off is also a deletion policy. The standard
		// /tmp history is separate and remains available for this container.
		_ = os.RemoveAll(dir)
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-retention)
	for _, ent := range ents {
		if ent.IsDir() || filepath.Ext(ent.Name()) != ".ansi" {
			continue
		}
		if st, err := ent.Info(); err == nil && st.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, ent.Name()))
		}
	}
}

func terminalHistoryPath(name string) string {
	return filepath.Join(terminalHistoryDir(), name+".ansi")
}

func readTerminalHistory(name string) ([]byte, bool) {
	if !session.ValidName(name) {
		return nil, false
	}
	b, err := os.ReadFile(terminalHistoryPath(name))
	return b, err == nil && len(b) > 0
}

func removeTerminalHistory(name string) { _ = os.Remove(terminalHistoryPath(name)) }

// runRecordTerminal is the stdin sink used by tmux pipe-pane. Keeping the cap in
// this helper means tmux never owns an unbounded shell redirection.
func runRecordTerminal(args []string) {
	if len(args) != 1 || !session.ValidName(args[0]) {
		return
	}
	_ = recordTerminal(args[0], os.Stdin)
}

func recordTerminal(name string, src io.Reader) error {
	dir := terminalHistoryDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := terminalHistoryPath(name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	// f is swapped by compaction (which closes the old descriptor), so close via the
	// variable, not the original handle.
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()

	buf := make([]byte, 32*1024)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return err
			}
			if st, err := f.Stat(); err == nil && st.Size() > terminalHistoryMaxBytes+(256<<10) {
				var cerr error
				if f, cerr = compactTerminalHistory(f, path, terminalHistoryMaxBytes); cerr != nil {
					return cerr
				}
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				var cerr error
				f, cerr = compactTerminalHistory(f, path, terminalHistoryMaxBytes)
				return cerr
			}
			return rerr
		}
	}
}

// compactTerminalHistory trims the file to its last `max` bytes and returns the
// handle to keep appending to: f itself when no compaction was needed, else a fresh
// descriptor (f is closed). On error the returned handle may be nil.
func compactTerminalHistory(f *os.File, path string, max int64) (*os.File, error) {
	st, err := f.Stat()
	if err != nil || st.Size() <= max {
		return f, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	in, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	_, err = in.Seek(st.Size()-max, io.SeekStart)
	if err != nil {
		in.Close()
		return nil, err
	}
	tail, err := io.ReadAll(in)
	in.Close()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, tail, 0o600); err != nil {
		return nil, err
	}
	// Re-open for subsequent pipe input (returned instead of overwriting *f, which
	// depended on os.File's private layout).
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
}
