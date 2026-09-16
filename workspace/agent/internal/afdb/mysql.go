package afdb

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// buildMySQLEnv builds the environment for mysql/mysqladmin/mysqld commands.
// pw="" omits MYSQL_PWD (used before the root password is set).
// If AF_DB_MYSQL_LIBS is set, prepends it to LD_LIBRARY_PATH.
func buildMySQLEnv(pw string) []string {
	// Strip any inherited MYSQL_PWD so we never send a stale password from the
	// caller's environment; we append our own value below when pw is non-empty.
	base := os.Environ()
	env := base[:0:len(base)]
	for _, e := range base {
		if !strings.HasPrefix(e, "MYSQL_PWD=") {
			env = append(env, e)
		}
	}
	if libs := os.Getenv("AF_DB_MYSQL_LIBS"); libs != "" {
		found := false
		for i, e := range env {
			if strings.HasPrefix(e, "LD_LIBRARY_PATH=") {
				env[i] = "LD_LIBRARY_PATH=" + libs + ":" + strings.TrimPrefix(e, "LD_LIBRARY_PATH=")
				found = true
				break
			}
		}
		if !found {
			env = append(env, "LD_LIBRARY_PATH="+libs)
		}
	}
	if pw != "" {
		env = append(env, "MYSQL_PWD="+pw)
	}
	return env
}

// mysqlQuery runs a SQL query via the mysql client on the socket.
// Uses MYSQL_PWD env (not -p flag) so the password does not appear in /proc.
func mysqlQuery(inst *Instance, pw, query string) (string, error) {
	binDir := filepath.Join(inst.Root, "bin")
	sockFile := mysqlSockFile(inst)
	cmd := exec.Command(filepath.Join(binDir, "mysql"),
		"--no-defaults", "-uroot", "--socket="+sockFile,
		"-N", "-B", "-e", query,
	)
	cmd.Env = buildMySQLEnv(pw)
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		return "", fmt.Errorf("mysql query %q: %w%s", query, err, stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

// ensureInstalledMySQL checks that <root>/bin/mysqld exists; if not, runs
// workspace-agent install-mysql <major>.
func ensureInstalledMySQL(root, major string) error {
	bin := filepath.Join(root, "bin", "mysqld")
	if _, err := os.Stat(bin); err == nil {
		return nil
	}
	fmt.Fprintf(os.Stderr, "af-db: mysql-%s not installed; installing...\n", major)
	self, err := os.Executable()
	if err != nil {
		self = "/usr/local/bin/workspace-agent"
	}
	cmd := exec.Command(self, "install-mysql", major)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return errInstall(fmt.Sprintf("install-mysql %s failed: %v", major, err))
	}
	if _, err := os.Stat(bin); err != nil {
		return errInstall(fmt.Sprintf("install-mysql %s completed but %s not found", major, bin))
	}
	return nil
}

// waitMySQLReady polls mysqladmin ping until ready or timeout elapses.
// pw="" is valid for the initial startup before the root password is set.
func waitMySQLReady(binDir, sockFile, pw string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command(filepath.Join(binDir, "mysqladmin"),
			"--no-defaults", "-uroot", "--socket="+sockFile, "ping",
		)
		cmd.Env = buildMySQLEnv(pw)
		if err := cmd.Run(); err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("mysqld not ready after %s", timeout)
}

// startMySQLServer initializes (if new datadir) and starts mysqld.
// Caller must hold the start lock.
func startMySQLServer(inst *Instance, persist bool) error {
	major := inst.Major
	root := inst.Root
	binDir := filepath.Join(root, "bin")

	var base string
	if persist {
		base = homeStateBase()
	} else {
		base = scratchBase()
	}
	datadir := filepath.Join(base, "mysql-"+major, "data")
	sockdir := filepath.Join(sockBase(), "mysql-"+major)
	sockFile := filepath.Join(sockdir, "mysql.sock")
	pidFile := filepath.Join(base, "mysql-"+major, "mysqld.pid")
	logFile := filepath.Join(base, "mysql-"+major+".log")

	inst.Datadir = datadir
	inst.Sockdir = sockdir
	inst.Persist = persist

	newInit := false
	if _, err := os.Stat(datadir); os.IsNotExist(err) {
		newInit = true
		if err := os.MkdirAll(datadir, 0o700); err != nil {
			return errStart(fmt.Sprintf("create mysql datadir: %v", err))
		}
		if err := os.MkdirAll(sockdir, 0o700); err != nil {
			return errStart(fmt.Sprintf("create mysql sockdir: %v", err))
		}

		initCmd := exec.Command(filepath.Join(binDir, "mysqld"),
			"--no-defaults",
			"--initialize-insecure",
			"--basedir="+root,
			"--datadir="+datadir,
			"--log-error="+logFile,
		)
		initCmd.Env = buildMySQLEnv("")
		if out, err := initCmd.CombinedOutput(); err != nil {
			return errStart(fmt.Sprintf("mysqld --initialize-insecure: %v\n%s", err, out))
		}
	} else if err != nil {
		return errStart(fmt.Sprintf("stat mysql datadir: %v", err))
	} else {
		if err := os.MkdirAll(sockdir, 0o700); err != nil {
			return errStart(fmt.Sprintf("create mysql sockdir: %v", err))
		}
	}

	port, err := pickPort()
	if err != nil {
		return errStart(fmt.Sprintf("pick port: %v", err))
	}
	inst.Port = port

	args := []string{
		"--no-defaults",
		"--basedir=" + root,
		"--datadir=" + datadir,
		"--socket=" + sockFile,
		"--pid-file=" + pidFile,
		"--log-error=" + logFile,
		"--bind-address=127.0.0.1",
		fmt.Sprintf("--port=%d", port),
		"--skip-name-resolve",
		"--mysqlx=0",
		"--innodb-buffer-pool-size=64M",
		"--performance-schema=0",
	}
	if !persist {
		args = append(args, "--innodb-flush-log-at-trx-commit=0")
	}

	srv := exec.Command(filepath.Join(binDir, "mysqld"), args...)
	srv.Env = buildMySQLEnv("")
	srv.Stdout = nil
	srv.Stderr = nil
	srv.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := srv.Start(); err != nil {
		return errStart(fmt.Sprintf("mysqld start: %v (log: %s)", err, logFile))
	}
	// Reap the child when it exits so it does not become a zombie.
	// waitPIDGone / isMySQLRunning use /proc and the pid file rather than Wait(),
	// so the reaper goroutine runs independently.
	go srv.Wait() //nolint:errcheck

	// Wait for mysqld to be ready (no password yet on first init).
	startingPW := ""
	if !newInit {
		startingPW = readPass(passPath("mysql", major))
	}
	if err := waitMySQLReady(binDir, sockFile, startingPW, 60*time.Second); err != nil {
		srv.Process.Kill() //nolint:errcheck
		return errStart(fmt.Sprintf("%v (log: %s)", err, logFile))
	}

	if newInit {
		// Generate a random password into memory first; write the pass file only
		// after ALTER USER succeeds so the file always matches MySQL's actual state.
		pp := passPath("mysql", major)
		pw, err := generatePassInMemory()
		if err != nil {
			srv.Process.Kill() //nolint:errcheck
			return errStart(fmt.Sprintf("generate mysql password: %v", err))
		}
		initSQL := fmt.Sprintf(
			"ALTER USER 'root'@'localhost' IDENTIFIED BY '%s';\n"+
				"CREATE USER IF NOT EXISTS 'root'@'127.0.0.1' IDENTIFIED BY '%s';\n"+
				"GRANT ALL ON *.* TO 'root'@'127.0.0.1' WITH GRANT OPTION;\n"+
				"FLUSH PRIVILEGES;\n",
			pw, pw,
		)
		pwSetCmd := exec.Command(filepath.Join(binDir, "mysql"),
			"--no-defaults", "-uroot", "--socket="+sockFile,
		)
		pwSetCmd.Stdin = strings.NewReader(initSQL)
		pwSetCmd.Env = buildMySQLEnv("")
		if out, err := pwSetCmd.CombinedOutput(); err != nil {
			srv.Process.Kill() //nolint:errcheck
			// Datadir is in an unknown state; remove it so the next `af-db up mysql`
			// re-initializes from scratch rather than trying to start with no password.
			_ = os.RemoveAll(datadir)
			return errStart(fmt.Sprintf("set mysql root password: %v\n%s", err, out))
		}
		// ALTER succeeded — now persist the password.
		if err := writePassFile(pp, pw); err != nil {
			srv.Process.Kill() //nolint:errcheck
			_ = os.RemoveAll(datadir)
			return errStart(fmt.Sprintf("write mysql pass file: %v", err))
		}
	}

	// Read the pid from the pid file.
	var livePID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p, err := readMySQLPID(pidFile); err == nil && p > 0 {
			livePID = p
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	inst.PID = livePID
	inst.StartedAt = time.Now()
	inst.LastUsedAt = time.Now()

	return withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return err
		}
		key := instanceKey(inst.Engine, inst.Major)
		r.Instances[key] = inst
		return writeRegistry(r)
	})
}

// stopMySQLServer shuts down mysqld via mysqladmin and waits for the pid to disappear.
// Only after the process is gone is it safe for the caller to touch the datadir.
func stopMySQLServer(inst *Instance) error {
	if inst.Datadir == "" {
		return nil
	}
	sockFile := mysqlSockFile(inst)
	pidFile := filepath.Join(filepath.Dir(inst.Datadir), "mysqld.pid")
	binDir := filepath.Join(inst.Root, "bin")

	livePID, _ := readMySQLPID(pidFile)
	if livePID <= 0 {
		livePID = inst.PID
	}

	pw := readPass(passPath("mysql", inst.Major))
	cmd := exec.Command(filepath.Join(binDir, "mysqladmin"),
		"--no-defaults", "-uroot", "--socket="+sockFile, "shutdown",
	)
	cmd.Env = buildMySQLEnv(pw)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// Not running → that's fine.
		if !isMySQLRunning(livePID, inst.Datadir) {
			return nil
		}
		if livePID <= 0 {
			return fmt.Errorf("mysqladmin shutdown failed and no pid known; datadir may be unsafe to remove: %w", err)
		}
		return fmt.Errorf("mysqladmin shutdown: %w", err)
	}
	if livePID > 0 {
		if !waitPIDGone(livePID, 15*time.Second) {
			return fmt.Errorf("mysqld pid %d did not disappear within 15 s", livePID)
		}
	}
	return nil
}

// ensureMySQLDatabase creates the named database if it does not already exist.
// Using IF NOT EXISTS makes this atomic against concurrent callers.
func ensureMySQLDatabase(inst *Instance, pw, dbName string) error {
	_, err := mysqlQuery(inst, pw, "CREATE DATABASE IF NOT EXISTS `"+dbName+"`")
	if err != nil {
		return fmt.Errorf("create mysql database %q: %w", dbName, err)
	}
	return nil
}

// countMySQLClientBackends returns the count of user connections, excluding
// the event_scheduler and the probe connection itself.
// Returns -1 on any error.
func countMySQLClientBackends(inst *Instance) int {
	if !isMySQLRunning(inst.PID, inst.Datadir) {
		return -1
	}
	pw := readPass(passPath("mysql", inst.Major))
	out, err := mysqlQuery(inst, pw,
		"SELECT count(*) FROM information_schema.processlist WHERE id <> connection_id() AND user <> 'event_scheduler'",
	)
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return -1
	}
	return n
}

// reconcileMySQL drops databases whose recorded working-copy directory no longer
// exists. Databases with dir=="" (explicitly named) are never dropped.
func reconcileMySQL(inst *Instance, pw string) error {
	if !isMySQLRunning(inst.PID, inst.Datadir) || len(inst.Databases) == 0 {
		return nil
	}
	var toDrop []string
	for name, dir := range inst.Databases {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			toDrop = append(toDrop, name)
		}
	}
	if len(toDrop) == 0 {
		return nil
	}

	var dropped []string
	for _, name := range toDrop {
		if _, err := mysqlQuery(inst, pw, "DROP DATABASE IF EXISTS `"+name+"`"); err != nil {
			fmt.Fprintf(os.Stderr, "af-db: mysql reconcile: drop %q: %v\n", name, err)
			continue
		}
		dropped = append(dropped, name)
	}
	if len(dropped) == 0 {
		return nil
	}
	return withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return err
		}
		key := instanceKey(inst.Engine, inst.Major)
		if i, ok := r.Instances[key]; ok {
			for _, name := range dropped {
				delete(i.Databases, name)
			}
		}
		return writeRegistry(r)
	})
}

// mysqlVersion returns the MySQL server version string via SELECT VERSION().
func mysqlVersion(inst *Instance) string {
	pw := readPass(passPath("mysql", inst.Major))
	out, err := mysqlQuery(inst, pw, "SELECT VERSION()")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
