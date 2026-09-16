package afdb

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// RunAFDB is the entry point for `workspace-agent af-db <args>`.
func RunAFDB(args []string) {
	if len(args) == 0 {
		printUsage()
		os.Exit(2)
	}
	verb := args[0]
	rest := args[1:]

	var err error
	switch verb {
	case "up":
		err = cmdUp(rest)
	case "url":
		err = cmdURL(rest)
	case "env":
		err = cmdEnv(rest)
	case "reset":
		err = cmdReset(rest)
	case "down":
		err = cmdDown(rest)
	case "status":
		err = cmdStatus(rest)
	default:
		fmt.Fprintf(os.Stderr, "af-db: unknown verb %q\n", verb)
		printUsage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "af-db:", err)
		if isExitErr(err) {
			os.Exit(exitCode(err))
		}
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `usage: af-db <verb> [options]

Verbs:
  up [--major N] [--persist]   ensure Postgres is installed and running
  url [--db=NAME] [--tcp]      print connection URL (installs/starts if needed)
  env [--db=NAME] [--tcp]      print export lines for AF_DB_URL_POSTGRES and DATABASE_URL
  reset [--db=NAME]            DROP + CREATE the database
  down [--purge]               stop the server; --purge removes the datadir
  status [--json]              show instance state`)
}

// afdbErr carries an exit code.
type afdbErr struct {
	code int
	msg  string
}

func (e *afdbErr) Error() string { return e.msg }

func isExitErr(err error) bool {
	_, ok := err.(*afdbErr)
	return ok
}

func exitCode(err error) int {
	if e, ok := err.(*afdbErr); ok {
		return e.code
	}
	return 1
}

func errUsage(msg string) error   { return &afdbErr{2, msg} }
func errInstall(msg string) error { return &afdbErr{3, msg} }
func errStart(msg string) error   { return &afdbErr{4, msg} }
func errNotRun(msg string) error  { return &afdbErr{5, msg} }

// ---- verb implementations ----

func cmdUp(args []string) error {
	major := DefaultMajor
	persist := false
	for _, a := range args {
		switch {
		case a == "--persist":
			persist = true
		case strings.HasPrefix(a, "--major="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--major="))
			if err != nil {
				return errUsage("--major: not a number")
			}
			major = n
		case a == "--major":
			// handled as two args; not supported in positional form
		case a == "postgres":
			// explicit engine, default is postgres
		default:
			return errUsage(fmt.Sprintf("unknown option: %s", a))
		}
	}
	inst, err := ensureUp("postgres", major, persist)
	if err != nil {
		return err
	}
	fmt.Printf("postgres-%d running on port %d\n", inst.Major, inst.Port)
	return nil
}

func cmdURL(args []string) error {
	dbName := ""
	tcp := false
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--db="):
			dbName = strings.TrimPrefix(a, "--db=")
		case a == "--tcp":
			tcp = true
		default:
			return errUsage(fmt.Sprintf("unknown option: %s", a))
		}
	}
	url, err := urlFor("postgres", DefaultMajor, dbName, tcp)
	if err != nil {
		return err
	}
	fmt.Println(url)
	return nil
}

func cmdEnv(args []string) error {
	dbName := ""
	tcp := false
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--db="):
			dbName = strings.TrimPrefix(a, "--db=")
		case a == "--tcp":
			tcp = true
		default:
			return errUsage(fmt.Sprintf("unknown option: %s", a))
		}
	}
	url, err := urlFor("postgres", DefaultMajor, dbName, tcp)
	if err != nil {
		return err
	}
	fmt.Printf("export AF_DB_URL_POSTGRES=%q\n", url)
	fmt.Printf("export DATABASE_URL=%q\n", url)
	return nil
}

func cmdReset(args []string) error {
	dbName := ""
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--db="):
			dbName = strings.TrimPrefix(a, "--db=")
		default:
			return errUsage(fmt.Sprintf("unknown option: %s", a))
		}
	}
	return resetDB("postgres", DefaultMajor, dbName)
}

func cmdDown(args []string) error {
	purge := false
	for _, a := range args {
		if a == "--purge" {
			purge = true
		} else {
			return errUsage(fmt.Sprintf("unknown option: %s", a))
		}
	}
	return stopInstance("postgres", DefaultMajor, purge)
}

func cmdStatus(args []string) error {
	asJSON := false
	for _, a := range args {
		if a == "--json" {
			asJSON = true
		} else {
			return errUsage(fmt.Sprintf("unknown option: %s", a))
		}
	}
	return showStatus("postgres", DefaultMajor, asJSON)
}

// ---- core logic ----

// ensureUp installs postgres if needed and starts it if stopped. Returns the running instance.
func ensureUp(engine string, major int, persist bool) (*Instance, error) {
	root := postgresRoot(major)
	if err := ensureInstalled(root, major); err != nil {
		return nil, err
	}
	var inst *Instance
	if err := withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return err
		}
		key := instanceKey(engine, major)
		inst = r.Instances[key]
		if inst == nil {
			inst = &Instance{
				Engine:    engine,
				Major:     major,
				Root:      root,
				Databases: make(map[string]string),
				Persist:   persist,
			}
		}
		inst.Root = root
		r.Instances[key] = inst
		return writeRegistry(r)
	}); err != nil {
		return nil, err
	}

	if !isRunning(inst.PID) {
		if err := startServer(inst, persist); err != nil {
			return nil, err
		}
	}
	return inst, nil
}

// urlFor ensures an instance is up and a database for the current working copy (or --db)
// exists, then returns the connection URL. Bumps lastUsedAt and reconciles.
func urlFor(engine string, major int, explicitDB string, tcp bool) (string, error) {
	inst, err := ensureUp(engine, major, false)
	if err != nil {
		return "", err
	}

	dir := ResolveDir()
	pw := readPass(passPath(major))

	var dbName string
	explicitlyNamed := explicitDB != ""
	if explicitlyNamed {
		dbName = explicitDB
	} else {
		dbName = DBNameFor(dir)
	}

	// Reconcile: drop databases whose recorded dir no longer exists.
	if err := reconcile(inst, pw); err != nil {
		fmt.Fprintf(os.Stderr, "af-db: reconcile warning: %v\n", err)
	}

	// Ensure the database exists.
	connStr := buildURL(inst, "postgres", pw, false)
	if err := ensureDatabase(connStr, dbName); err != nil {
		return "", fmt.Errorf("create database: %w", err)
	}

	// Record the database in the registry.
	recordedDir := dir
	if explicitlyNamed {
		recordedDir = "" // explicitly named = no dir = reconcile never drops it
	}
	if err := withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return err
		}
		key := instanceKey(engine, major)
		if inst2, ok := r.Instances[key]; ok {
			if inst2.Databases == nil {
				inst2.Databases = make(map[string]string)
			}
			inst2.Databases[dbName] = recordedDir
			inst2.LastUsedAt = time.Now()
		}
		return writeRegistry(r)
	}); err != nil {
		return "", err
	}
	inst.LastUsedAt = time.Now()

	return buildURL(inst, dbName, pw, tcp), nil
}

// resetDB drops and recreates the database.
func resetDB(engine string, major int, explicitDB string) error {
	var inst *Instance
	if err := withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return err
		}
		inst = r.Instances[instanceKey(engine, major)]
		return nil
	}); err != nil {
		return err
	}
	if inst == nil || !isRunning(inst.PID) {
		return errNotRun("postgres is not running; run 'af-db up' first")
	}
	pw := readPass(passPath(major))
	var dbName string
	if explicitDB != "" {
		dbName = explicitDB
	} else {
		dbName = DBNameFor(ResolveDir())
	}
	connStr := buildURL(inst, "postgres", pw, false)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, dbName)); err != nil {
		return fmt.Errorf("drop database: %w", err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE "%s"`, dbName)); err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	fmt.Printf("database %q reset\n", dbName)
	return nil
}

// stopInstance stops the server; if purge=true, removes the datadir after the pid is gone.
func stopInstance(engine string, major int, purge bool) error {
	var inst *Instance
	if err := withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return err
		}
		if r.Instances != nil {
			if i, ok := r.Instances[instanceKey(engine, major)]; ok {
				instCopy := *i
				inst = &instCopy
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if inst == nil {
		fmt.Println("af-db: no instance found; nothing to stop")
		return nil
	}
	if err := stopServer(inst); err != nil {
		return err
	}
	if purge && inst.Datadir != "" {
		if err := os.RemoveAll(inst.Datadir); err != nil {
			fmt.Fprintf(os.Stderr, "af-db: purge datadir: %v\n", err)
		} else {
			fmt.Printf("datadir %s removed\n", inst.Datadir)
		}
	}
	// Clear PID and databases from registry.
	return withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return err
		}
		key := instanceKey(engine, major)
		if i, ok := r.Instances[key]; ok {
			i.PID = 0
			i.Port = 0
			if purge {
				i.Databases = make(map[string]string)
			}
		}
		return writeRegistry(r)
	})
}

func showStatus(engine string, major int, asJSON bool) error {
	var inst *Instance
	if err := withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return err
		}
		if r.Instances != nil {
			if i, ok := r.Instances[instanceKey(engine, major)]; ok {
				instCopy := *i
				inst = &instCopy
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if inst == nil {
		if asJSON {
			fmt.Println(`{"state":"not-configured"}`)
		} else {
			fmt.Printf("postgres-%d: not configured\n", major)
		}
		return nil
	}
	running := isRunning(inst.PID)
	state := "stopped"
	if running {
		state = "running"
	}
	if asJSON {
		out := map[string]any{
			"engine":     inst.Engine,
			"major":      inst.Major,
			"state":      state,
			"pid":        inst.PID,
			"port":       inst.Port,
			"sockdir":    inst.Sockdir,
			"datadir":    inst.Datadir,
			"databases":  inst.Databases,
			"lastUsedAt": inst.LastUsedAt,
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Printf("postgres-%d: %s", major, state)
		if running {
			fmt.Printf("  port=%d  sock=%s  pid=%d", inst.Port, inst.Sockdir, inst.PID)
		}
		fmt.Println()
		for name, dir := range inst.Databases {
			if dir == "" {
				fmt.Printf("  db: %s (shared)\n", name)
			} else {
				fmt.Printf("  db: %s  dir=%s\n", name, dir)
			}
		}
	}
	return nil
}

// ---- install ----

// ensureInstalled checks that <root>/bin/postgres exists; if not, runs
// workspace-agent install-postgres <major> to install it.
func ensureInstalled(root string, major int) error {
	bin := filepath.Join(root, "bin", "postgres")
	if _, err := os.Stat(bin); err == nil {
		return nil // already installed
	}
	fmt.Fprintf(os.Stderr, "af-db: postgres-%d not installed; installing...\n", major)
	// exec ourselves (workspace-agent) with install-postgres <major>.
	self, err := os.Executable()
	if err != nil {
		self = "/usr/local/bin/workspace-agent"
	}
	cmd := exec.Command(self, "install-postgres", strconv.Itoa(major))
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// Determine sha info for the user.
		return errInstall(fmt.Sprintf(
			"install-postgres %d failed: %v (see stderr for details)", major, err))
	}
	if _, err := os.Stat(bin); err != nil {
		return errInstall(fmt.Sprintf(
			"install-postgres %d completed but %s not found", major, bin))
	}
	return nil
}

// ---- server lifecycle ----

// startServer runs initdb (if datadir absent), then pg_ctl start.
// The caller must have already checked that the instance is not running.
func startServer(inst *Instance, persist bool) error {
	major := inst.Major
	root := inst.Root
	binDir := filepath.Join(root, "bin")

	// Determine paths.
	datadir := filepath.Join(scratchBase(), fmt.Sprintf("postgres-%d", major), "data")
	sockdir := filepath.Join(sockBase(), fmt.Sprintf("postgres-%d", major))
	lp := logPath(root, major)

	inst.Datadir = datadir
	inst.Sockdir = sockdir
	inst.Persist = persist

	// initdb if datadir missing.
	if _, err := os.Stat(datadir); os.IsNotExist(err) {
		if err := os.MkdirAll(datadir, 0o700); err != nil {
			return errStart(fmt.Sprintf("create datadir: %v", err))
		}
		if err := os.MkdirAll(sockdir, 0o700); err != nil {
			return errStart(fmt.Sprintf("create sockdir: %v", err))
		}

		pp := passPath(major)
		pw, err := generatePass(pp)
		if err != nil {
			return errStart(fmt.Sprintf("generate password: %v", err))
		}
		_ = pw

		// Write a temp pwfile for initdb (it must end with a newline, which generatePass ensures).
		cmd := exec.Command(filepath.Join(binDir, "initdb"),
			"--auth=scram-sha-256",
			"--auth-local=scram-sha-256",
			"-U", "postgres",
			fmt.Sprintf("--pwfile=%s", pp),
			"-D", datadir,
		)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return errStart(fmt.Sprintf("initdb: %v (log: %s)", err, lp))
		}
	} else if err != nil {
		return errStart(fmt.Sprintf("stat datadir: %v", err))
	}

	// Pick a free port.
	port, err := pickPort()
	if err != nil {
		return errStart(fmt.Sprintf("pick port: %v", err))
	}
	inst.Port = port

	// Build server flags.
	fsync := "off"
	if persist {
		fsync = "on"
	}
	serverFlags := fmt.Sprintf(
		"-k %s -h 127.0.0.1 -p %d -c shared_buffers=32MB -c max_connections=50 -c fsync=%s",
		sockdir, port, fsync)

	if err := os.MkdirAll(sockdir, 0o700); err != nil {
		return errStart(fmt.Sprintf("create sockdir: %v", err))
	}

	cmd := exec.Command(filepath.Join(binDir, "pg_ctl"),
		"-w", "start",
		"-D", datadir,
		"-l", lp,
		"-o", serverFlags,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return errStart(fmt.Sprintf("pg_ctl start: %v (log: %s)", err, lp))
	}

	// Read the postmaster PID.
	pid, err := readPostmasterPID(datadir)
	if err != nil {
		// Not fatal — we can still use the instance.
		fmt.Fprintf(os.Stderr, "af-db: warning: could not read postmaster.pid: %v\n", err)
	}
	inst.PID = pid
	inst.StartedAt = time.Now()
	inst.LastUsedAt = time.Now()

	// Write back to registry.
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

// stopServer runs pg_ctl stop -m fast and waits for the postmaster pid to disappear.
// Only THEN is the datadir safe to touch.
func stopServer(inst *Instance) error {
	if inst.Datadir == "" {
		return nil
	}
	binDir := filepath.Join(inst.Root, "bin")
	cmd := exec.Command(filepath.Join(binDir, "pg_ctl"),
		"stop", "-m", "fast",
		"-D", inst.Datadir,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if !isRunning(inst.PID) {
			// Already stopped; pg_ctl exit 1 means not running — that's fine.
			return nil
		}
		return fmt.Errorf("pg_ctl stop: %w", err)
	}
	// Wait for pid to disappear before returning.
	if inst.PID > 0 {
		if !waitPIDGone(inst.PID, 15*time.Second) {
			return fmt.Errorf("postmaster pid %d did not disappear within 15 s", inst.PID)
		}
	}
	return nil
}

// readPostmasterPID parses the first line of <datadir>/postmaster.pid.
func readPostmasterPID(datadir string) (int, error) {
	b, err := os.ReadFile(filepath.Join(datadir, "postmaster.pid"))
	if err != nil {
		return 0, err
	}
	line := strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)[0]
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		return 0, fmt.Errorf("parse postmaster.pid: %w", err)
	}
	return pid, nil
}

// ---- database management via pgx ----

// ensureDatabase creates the database if it does not already exist.
func ensureDatabase(connStr, dbName string) error {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer conn.Close(ctx)

	var exists bool
	err = conn.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", dbName,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check database existence: %w", err)
	}
	if exists {
		return nil
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE "%s"`, dbName)); err != nil {
		return fmt.Errorf("create database %q: %w", dbName, err)
	}
	return nil
}

// reconcile drops databases whose recorded directory no longer exists on disk.
// Databases with dir=="" (explicitly named) are never dropped.
func reconcile(inst *Instance, pw string) error {
	if !isRunning(inst.PID) || len(inst.Databases) == 0 {
		return nil
	}
	var toDrop []string
	for name, dir := range inst.Databases {
		if dir == "" {
			continue // explicitly named, never drop
		}
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			toDrop = append(toDrop, name)
		}
	}
	if len(toDrop) == 0 {
		return nil
	}

	connStr := buildURL(inst, "postgres", pw, false)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		return fmt.Errorf("connect for reconcile: %w", err)
	}
	defer conn.Close(ctx)

	var dropped []string
	for _, name := range toDrop {
		if _, err := conn.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, name)); err != nil {
			fmt.Fprintf(os.Stderr, "af-db: reconcile: drop %q: %v\n", name, err)
			continue
		}
		dropped = append(dropped, name)
	}

	if len(dropped) == 0 {
		return nil
	}
	// Remove dropped databases from the registry.
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

// CountClientBackends returns the number of client backends on the running instance.
// Returns -1 on any error.
func CountClientBackends(inst *Instance) int {
	if !isRunning(inst.PID) {
		return -1
	}
	pw := readPass(passPath(inst.Major))
	connStr := buildURL(inst, "postgres", pw, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		return -1
	}
	defer conn.Close(ctx)
	var n int
	if err := conn.QueryRow(ctx,
		`SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend'`,
	).Scan(&n); err != nil {
		return -1
	}
	return n
}
