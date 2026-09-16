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
  up [--major N] [--persist]         ensure Postgres is installed and running
  url [--major N] [--db=NAME] [--tcp] print connection URL (installs/starts if needed)
  env [--major N] [--db=NAME] [--tcp] print export lines for AF_DB_URL_POSTGRES / DATABASE_URL
  reset [--major N] [--db=NAME]       DROP + CREATE the database
  down [--major N] [--purge]          stop the server; --purge removes the datadir
  status [--major N] [--json]         show instance state

  --major defaults to 17`)
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

// parseMajorFromArgs extracts --major N (both --major=N and space forms) from args.
// Returns the major and the remaining args (with --major and its value removed).
func parseMajorFromArgs(args []string) (int, []string, error) {
	major := DefaultMajor
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "--major="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--major="))
			if err != nil {
				return 0, nil, errUsage("--major: not a number")
			}
			major = n
		case a == "--major":
			if i+1 >= len(args) {
				return 0, nil, errUsage("--major requires a value")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				return 0, nil, errUsage("--major: not a number")
			}
			major = n
			i++
		default:
			rest = append(rest, a)
		}
	}
	return major, rest, nil
}

// ---- verb implementations ----

func cmdUp(args []string) error {
	major, args, err := parseMajorFromArgs(args)
	if err != nil {
		return err
	}
	persist := false
	for _, a := range args {
		switch {
		case a == "--persist":
			persist = true
		case a == "postgres":
			// explicit engine name; default is postgres
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
	major, args, err := parseMajorFromArgs(args)
	if err != nil {
		return err
	}
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
	if dbName != "" {
		if err := validateExplicitDB(dbName); err != nil {
			return err
		}
	}
	url, err := urlFor("postgres", major, dbName, tcp)
	if err != nil {
		return err
	}
	fmt.Println(url)
	return nil
}

func cmdEnv(args []string) error {
	major, args, err := parseMajorFromArgs(args)
	if err != nil {
		return err
	}
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
	if dbName != "" {
		if err := validateExplicitDB(dbName); err != nil {
			return err
		}
	}
	url, err := urlFor("postgres", major, dbName, tcp)
	if err != nil {
		return err
	}
	fmt.Printf("export AF_DB_URL_POSTGRES=%q\n", url)
	fmt.Printf("export DATABASE_URL=%q\n", url)
	return nil
}

func cmdReset(args []string) error {
	major, args, err := parseMajorFromArgs(args)
	if err != nil {
		return err
	}
	dbName := ""
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--db="):
			dbName = strings.TrimPrefix(a, "--db=")
		default:
			return errUsage(fmt.Sprintf("unknown option: %s", a))
		}
	}
	if dbName != "" {
		if err := validateExplicitDB(dbName); err != nil {
			return err
		}
	}
	return resetDB("postgres", major, dbName)
}

func cmdDown(args []string) error {
	major, args, err := parseMajorFromArgs(args)
	if err != nil {
		return err
	}
	purge := false
	for _, a := range args {
		if a == "--purge" {
			purge = true
		} else {
			return errUsage(fmt.Sprintf("unknown option: %s", a))
		}
	}
	return stopInstance("postgres", major, purge)
}

func cmdStatus(args []string) error {
	major, args, err := parseMajorFromArgs(args)
	if err != nil {
		return err
	}
	asJSON := false
	for _, a := range args {
		if a == "--json" {
			asJSON = true
		} else {
			return errUsage(fmt.Sprintf("unknown option: %s", a))
		}
	}
	return showStatus("postgres", major, asJSON)
}

// ---- core logic ----

// ensureUp installs Postgres if needed and starts it if stopped.
// Concurrent calls are serialized by a per-(engine,major) start lock so only one
// initdb + pg_ctl start ever runs at a time.
func ensureUp(engine string, major int, persist bool) (*Instance, error) {
	root := postgresRoot(major)
	if err := ensureInstalled(root, major); err != nil {
		return nil, err
	}
	key := instanceKey(engine, major)

	// Ensure a registry entry exists before the start lock.
	if err := withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return err
		}
		if r.Instances[key] == nil {
			r.Instances[key] = &Instance{
				Engine:    engine,
				Major:     major,
				Root:      root,
				Databases: make(map[string]string),
			}
			return writeRegistry(r)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Serialize concurrent starts: only one goroutine/process runs initdb+pg_ctl at a time.
	if err := withStartLock(engine, major, func() error {
		// Re-read under start lock — a racing caller may have already started the server.
		var current *Instance
		_ = withLock(func() error {
			r, _ := readRegistry()
			if i, ok := r.Instances[key]; ok {
				cp := *i
				current = &cp
			}
			return nil
		})
		if current == nil {
			current = &Instance{Engine: engine, Major: major, Root: root,
				Databases: make(map[string]string)}
		}
		if isPGRunning(current.PID, current.Datadir) {
			return nil
		}
		// When restarting a stopped instance, preserve its persist setting unless
		// the caller is explicitly requesting persist (af-db up --persist).
		effectivePersist := current.Persist
		if persist {
			effectivePersist = true
		}
		return startServer(current, effectivePersist)
	}); err != nil {
		return nil, err
	}

	// Re-read the final state after start.
	var inst *Instance
	_ = withLock(func() error {
		r, _ := readRegistry()
		if i, ok := r.Instances[key]; ok {
			cp := *i
			inst = &cp
		}
		return nil
	})
	if inst == nil {
		return nil, fmt.Errorf("postgres-%d: not in registry after start", major)
	}

	// Reconcile stale databases (directories that no longer exist).
	pw := readPass(passPath(major))
	if err := reconcile(inst, pw); err != nil {
		fmt.Fprintf(os.Stderr, "af-db: reconcile warning: %v\n", err)
	}

	return inst, nil
}

// urlFor ensures an instance is up, a database exists for the current working copy
// (or --db), bumps lastUsedAt, and returns the connection URL.
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

	// Ensure the database exists.
	connStr := buildURL(inst, "postgres", pw, false)
	if err := ensureDatabase(connStr, dbName); err != nil {
		return "", fmt.Errorf("create database: %w", err)
	}

	// Record the database and bump lastUsedAt.
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
		if i, ok := r.Instances[instanceKey(engine, major)]; ok {
			cp := *i
			inst = &cp
		}
		return nil
	}); err != nil {
		return err
	}
	if inst == nil || !isPGRunning(inst.PID, inst.Datadir) {
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
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{dbName}.Sanitize()); err != nil {
		return fmt.Errorf("drop database: %w", err)
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{dbName}.Sanitize()); err != nil {
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
		if i, ok := r.Instances[instanceKey(engine, major)]; ok {
			cp := *i
			inst = &cp
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
		if i, ok := r.Instances[instanceKey(engine, major)]; ok {
			cp := *i
			inst = &cp
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
	running := isPGRunning(inst.PID, inst.Datadir)
	state := "stopped"
	if running {
		state = "running"
		// Reconcile stale databases while we have a live server.
		pw := readPass(passPath(major))
		if err := reconcile(inst, pw); err != nil {
			fmt.Fprintf(os.Stderr, "af-db: reconcile warning: %v\n", err)
		}
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
		return nil
	}
	fmt.Fprintf(os.Stderr, "af-db: postgres-%d not installed; installing...\n", major)
	self, err := os.Executable()
	if err != nil {
		self = "/usr/local/bin/workspace-agent"
	}
	cmd := exec.Command(self, "install-postgres", strconv.Itoa(major))
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return errInstall(fmt.Sprintf("install-postgres %d failed: %v", major, err))
	}
	if _, err := os.Stat(bin); err != nil {
		return errInstall(fmt.Sprintf("install-postgres %d completed but %s not found", major, bin))
	}
	return nil
}

// ---- server lifecycle ----

// startServer runs initdb (if datadir absent), then pg_ctl start.
// Caller must hold the start lock.
func startServer(inst *Instance, persist bool) error {
	major := inst.Major
	root := inst.Root
	binDir := filepath.Join(root, "bin")

	// Choose datadir base: home (persistent) or scratch (wiped on stop).
	var base string
	if persist {
		base = homeStateBase()
	} else {
		base = scratchBase()
	}
	datadir := filepath.Join(base, fmt.Sprintf("postgres-%d", major), "data")
	sockdir := filepath.Join(sockBase(), fmt.Sprintf("postgres-%d", major))
	logFile := filepath.Join(base, fmt.Sprintf("postgres-%d.log", major))

	inst.Datadir = datadir
	inst.Sockdir = sockdir
	inst.Persist = persist

	// Run initdb when the datadir is absent.
	if _, err := os.Stat(datadir); os.IsNotExist(err) {
		if err := os.MkdirAll(datadir, 0o700); err != nil {
			return errStart(fmt.Sprintf("create datadir: %v", err))
		}
		if err := os.MkdirAll(sockdir, 0o700); err != nil {
			return errStart(fmt.Sprintf("create sockdir: %v", err))
		}

		pp := passPath(major)
		if _, err := generatePass(pp); err != nil {
			return errStart(fmt.Sprintf("generate password: %v", err))
		}

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
			return errStart(fmt.Sprintf("initdb: %v (log: %s)", err, logFile))
		}
	} else if err != nil {
		return errStart(fmt.Sprintf("stat datadir: %v", err))
	}

	port, err := pickPort()
	if err != nil {
		return errStart(fmt.Sprintf("pick port: %v", err))
	}
	inst.Port = port

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
		"-l", logFile,
		"-o", serverFlags,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return errStart(fmt.Sprintf("pg_ctl start: %v (log: %s)", err, logFile))
	}

	pid, err := readPostmasterPID(datadir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "af-db: warning: could not read postmaster.pid: %v\n", err)
	}
	inst.PID = pid
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

// stopServer runs pg_ctl stop -m fast and waits for the postmaster pid to disappear.
// Only THEN is it safe for the caller to touch the datadir.
func stopServer(inst *Instance) error {
	if inst.Datadir == "" {
		return nil
	}

	// Resolve the authoritative pid from postmaster.pid — not the (possibly stale) registry pid.
	livePID, pidErr := readPostmasterPID(inst.Datadir)
	if pidErr != nil || livePID <= 0 {
		livePID = inst.PID
	}

	binDir := filepath.Join(inst.Root, "bin")
	cmd := exec.Command(filepath.Join(binDir, "pg_ctl"),
		"stop", "-m", "fast",
		"-D", inst.Datadir,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// pg_ctl exits non-zero when the server is already stopped — that is fine.
		if !isPGRunning(livePID, inst.Datadir) {
			return nil
		}
		// Server is still up but stop failed, and we do not have a pid to wait on.
		if livePID <= 0 {
			return fmt.Errorf("pg_ctl stop failed and no postmaster pid known; datadir may be unsafe to remove: %w", err)
		}
		return fmt.Errorf("pg_ctl stop: %w", err)
	}
	if livePID > 0 {
		if !waitPIDGone(livePID, 15*time.Second) {
			return fmt.Errorf("postmaster pid %d did not disappear within 15 s", livePID)
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
	if err := conn.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", dbName,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check database existence: %w", err)
	}
	if exists {
		return nil
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{dbName}.Sanitize()); err != nil {
		return fmt.Errorf("create database %q: %w", dbName, err)
	}
	return nil
}

// reconcile drops databases whose recorded directory no longer exists on disk.
// Databases with dir=="" (explicitly named) are never dropped.
func reconcile(inst *Instance, pw string) error {
	if !isPGRunning(inst.PID, inst.Datadir) || len(inst.Databases) == 0 {
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
		if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()); err != nil {
			fmt.Fprintf(os.Stderr, "af-db: reconcile: drop %q: %v\n", name, err)
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

// CountClientBackends returns the number of client backends connected to the instance,
// excluding this monitoring connection itself. Returns -1 on any error.
func CountClientBackends(inst *Instance) int {
	if !isPGRunning(inst.PID, inst.Datadir) {
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
		`SELECT count(*) FROM pg_stat_activity
		 WHERE backend_type = 'client backend' AND pid <> pg_backend_pid()`,
	).Scan(&n); err != nil {
		return -1
	}
	return n
}
