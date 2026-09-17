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
	fmt.Fprintln(os.Stderr, `usage: af-db <verb> [engine] [options]

Engines: postgres (default), mysql

Verbs:
  up [engine] [--major N] [--persist]                  ensure engine is installed and running
  url [engine] [--major N] [--db=NAME] [--tcp]          print connection URL (installs/starts if needed)
      [--format=go-dsn]
  env [engine] [--major N] [--db=NAME] [--tcp]          print export lines for AF_DB_URL_* / DATABASE_URL
  reset [engine] [--major N] [--db=NAME]                 DROP + CREATE the database
  down [engine] [--major N] [--purge]                    stop the server; --purge removes the datadir
  status [engine] [--major N] [--json]                   show instance state (omit engine for all)

  --major defaults to 17 for postgres, 8.4 for mysql

Exit codes: 0=ok  2=usage  3=install failed  4=server failed  5=not running  6=memory gate`)
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
func errMemGate(msg string) error { return &afdbErr{6, msg} }

// defaultMajorForEngine returns the default major version string for an engine.
func defaultMajorForEngine(engine string) string {
	if engine == "mysql" {
		return DefaultMySQLMajor
	}
	return DefaultMajor
}

// parseEngineAndMajor extracts the optional engine positional arg and --major N flag.
// Returns (engine, major, rest, error). Unrecognised args remain in rest.
func parseEngineAndMajor(args []string, defaultEngine string) (engine, major string, rest []string, err error) {
	engine = defaultEngine
	engineSet := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "postgres" || a == "mysql") && !engineSet:
			engine = a
			engineSet = true
		case strings.HasPrefix(a, "--major="):
			major = strings.TrimPrefix(a, "--major=")
			if major == "" {
				return "", "", nil, errUsage("--major requires a value")
			}
		case a == "--major":
			if i+1 >= len(args) {
				return "", "", nil, errUsage("--major requires a value")
			}
			i++
			major = args[i]
		default:
			rest = append(rest, a)
		}
	}
	if major == "" {
		major = defaultMajorForEngine(engine)
	}
	return engine, major, rest, nil
}

// checkMemoryGate returns exit-6 if the cgroup memory limit is below 1 GiB.
// Disabled by AF_DB_MEM_GATE=0.
func checkMemoryGate() error {
	if os.Getenv("AF_DB_MEM_GATE") == "0" {
		return nil
	}
	b, err := os.ReadFile("/sys/fs/cgroup/memory.max")
	if err != nil {
		return nil
	}
	s := strings.TrimSpace(string(b))
	if s == "max" {
		return nil
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return nil
	}
	const oneGiB = 1024 * 1024 * 1024
	if n < oneGiB {
		return errMemGate(fmt.Sprintf(
			"memory.max=%d < 1 GiB; MySQL requires at least 1 GiB (set AF_DB_MEM_GATE=0 to skip)", n,
		))
	}
	return nil
}

// ---- verb implementations ----

func cmdUp(args []string) error {
	engine, major, args, err := parseEngineAndMajor(args, "postgres")
	if err != nil {
		return err
	}
	persist := false
	for _, a := range args {
		switch a {
		case "--persist":
			persist = true
		default:
			return errUsage(fmt.Sprintf("unknown option: %s", a))
		}
	}
	if engine == "mysql" {
		if err := checkMemoryGate(); err != nil {
			return err
		}
	}
	inst, err := ensureUp(engine, major, persist)
	if err != nil {
		return err
	}
	fmt.Printf("%s-%s running on port %d\n", engine, major, inst.Port)
	return nil
}

func cmdURL(args []string) error {
	engine, major, args, err := parseEngineAndMajor(args, "postgres")
	if err != nil {
		return err
	}
	dbName := ""
	tcp := false
	goDSN := false
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--db="):
			dbName = strings.TrimPrefix(a, "--db=")
		case a == "--tcp":
			tcp = true
		case a == "--format=go-dsn":
			goDSN = true
		default:
			return errUsage(fmt.Sprintf("unknown option: %s", a))
		}
	}
	if dbName != "" {
		if err := validateExplicitDB(dbName); err != nil {
			return err
		}
	}
	url, err := urlFor(engine, major, dbName, tcp, goDSN)
	if err != nil {
		return err
	}
	fmt.Println(url)
	return nil
}

func cmdEnv(args []string) error {
	engine, major, args, err := parseEngineAndMajor(args, "postgres")
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
	url, err := urlFor(engine, major, dbName, tcp, false)
	if err != nil {
		return err
	}
	if engine == "mysql" {
		fmt.Printf("export AF_DB_URL_MYSQL=%q\n", url)
	} else {
		fmt.Printf("export AF_DB_URL_POSTGRES=%q\n", url)
	}
	fmt.Printf("export DATABASE_URL=%q\n", url)
	return nil
}

func cmdReset(args []string) error {
	engine, major, args, err := parseEngineAndMajor(args, "postgres")
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
	return resetDB(engine, major, dbName)
}

func cmdDown(args []string) error {
	engine, major, args, err := parseEngineAndMajor(args, "postgres")
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
	return stopInstance(engine, major, purge)
}

func cmdStatus(args []string) error {
	// If no engine arg and no --major, show all instances.
	engine, major, args2, err := parseEngineAndMajor(args, "")
	if err != nil {
		return err
	}
	asJSON := false
	for _, a := range args2 {
		if a == "--json" {
			asJSON = true
		} else {
			return errUsage(fmt.Sprintf("unknown option: %s", a))
		}
	}
	if engine == "" {
		return showAllStatus(asJSON)
	}
	return showStatus(engine, major, asJSON)
}

// ---- core logic ----

// ensureUp installs the engine if needed and starts it if stopped.
// Concurrent calls are serialized by a per-(engine,major) start lock.
func ensureUp(engine, major string, persist bool) (*Instance, error) {
	var root string
	if engine == "mysql" {
		root = mysqlRoot(major)
		if err := ensureInstalledMySQL(root, major); err != nil {
			return nil, err
		}
	} else {
		root = postgresRoot(major)
		if err := ensureInstalled(root, major); err != nil {
			return nil, err
		}
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

	// Serialize concurrent starts.
	if err := withStartLock(engine, major, func() error {
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
		if isInstanceRunning(current) {
			return nil
		}
		effectivePersist := current.Persist
		if persist {
			effectivePersist = true
		}
		if engine == "mysql" {
			return startMySQLServer(current, effectivePersist)
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
		return nil, fmt.Errorf("%s-%s: not in registry after start", engine, major)
	}

	pw := readPass(passPath(engine, major))
	if engine == "mysql" {
		if err := reconcileMySQL(inst, pw); err != nil {
			fmt.Fprintf(os.Stderr, "af-db: reconcile warning: %v\n", err)
		}
	} else {
		if err := reconcile(inst, pw); err != nil {
			fmt.Fprintf(os.Stderr, "af-db: reconcile warning: %v\n", err)
		}
	}

	return inst, nil
}

// urlFor ensures an instance is up, a database exists, bumps lastUsedAt, and
// returns the connection URL. goDSN=true returns a go-sql-driver DSN for MySQL
// (for Postgres the URL and DSN are identical).
func urlFor(engine, major, explicitDB string, tcp, goDSN bool) (string, error) {
	inst, err := ensureUp(engine, major, false)
	if err != nil {
		return "", err
	}

	dir := ResolveDir()
	pw := readPass(passPath(engine, major))

	var dbName string
	explicitlyNamed := explicitDB != ""
	if explicitlyNamed {
		dbName = explicitDB
	} else {
		dbName = DBNameFor(dir)
	}

	if engine == "mysql" {
		if err := ensureMySQLDatabase(inst, pw, dbName); err != nil {
			return "", fmt.Errorf("create mysql database: %w", err)
		}
	} else {
		connStr := buildURL(inst, "postgres", pw, false)
		if err := ensureDatabase(connStr, dbName); err != nil {
			return "", fmt.Errorf("create database: %w", err)
		}
	}

	// Record the database and bump lastUsedAt.
	recordedDir := dir
	if explicitlyNamed {
		recordedDir = ""
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

	if engine == "mysql" {
		if goDSN {
			return buildMySQLGoDSN(inst, dbName, pw, tcp), nil
		}
		return buildMySQLURL(inst, dbName, pw, tcp), nil
	}
	// For Postgres, go-dsn and URL are the same (pgx accepts both).
	return buildURL(inst, dbName, pw, tcp), nil
}

// resetDB drops and recreates the working-copy (or explicit) database.
func resetDB(engine, major string, explicitDB string) error {
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
	if inst == nil || !isInstanceRunning(inst) {
		return errNotRun(fmt.Sprintf("%s is not running; run 'af-db up' first", engine))
	}
	pw := readPass(passPath(engine, major))
	var dbName string
	if explicitDB != "" {
		dbName = explicitDB
	} else {
		dbName = DBNameFor(ResolveDir())
	}

	if engine == "mysql" {
		if _, err := mysqlQuery(inst, pw, "DROP DATABASE IF EXISTS `"+dbName+"`"); err != nil {
			return fmt.Errorf("drop mysql database: %w", err)
		}
		if _, err := mysqlQuery(inst, pw, "CREATE DATABASE `"+dbName+"`"); err != nil {
			return fmt.Errorf("create mysql database: %w", err)
		}
	} else {
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
	}
	fmt.Printf("database %q reset\n", dbName)
	return nil
}

// stopInstance stops the server; if purge=true, removes the datadir after the pid is gone.
func stopInstance(engine, major string, purge bool) error {
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
	if engine == "mysql" {
		if err := stopMySQLServer(inst); err != nil {
			return err
		}
	} else {
		if err := stopServer(inst); err != nil {
			return err
		}
	}
	if purge && inst.Datadir != "" {
		// For mysql, datadir is <base>/mysql-<major>/data; remove <base>/mysql-<major>/ entirely.
		var target string
		if engine == "mysql" {
			target = filepath.Dir(inst.Datadir)
		} else {
			target = inst.Datadir
		}
		if err := os.RemoveAll(target); err != nil {
			fmt.Fprintf(os.Stderr, "af-db: purge: %v\n", err)
		} else {
			fmt.Printf("datadir %s removed\n", target)
		}
	}
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

func showStatus(engine, major string, asJSON bool) error {
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
			fmt.Printf("%s-%s: not configured\n", engine, major)
		}
		return nil
	}
	running := isInstanceRunning(inst)
	state := "stopped"
	if running {
		state = "running"
		pw := readPass(passPath(engine, major))
		if engine == "mysql" {
			if err := reconcileMySQL(inst, pw); err != nil {
				fmt.Fprintf(os.Stderr, "af-db: reconcile warning: %v\n", err)
			}
		} else {
			if err := reconcile(inst, pw); err != nil {
				fmt.Fprintf(os.Stderr, "af-db: reconcile warning: %v\n", err)
			}
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
		if running {
			out["version"] = instanceVersion(inst)
			out["rssBytes"] = rssForInstance(inst)
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Printf("%s-%s: %s", engine, major, state)
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

func showAllStatus(asJSON bool) error {
	var instances []*Instance
	_ = withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return err
		}
		for _, i := range r.Instances {
			cp := *i
			instances = append(instances, &cp)
		}
		return nil
	})
	if len(instances) == 0 {
		if asJSON {
			fmt.Println(`{"instances":[]}`)
		} else {
			fmt.Println("af-db: no instances configured")
		}
		return nil
	}
	if asJSON {
		var rows []map[string]any
		for _, inst := range instances {
			running := isInstanceRunning(inst)
			state := "stopped"
			if running {
				state = "running"
			}
			row := map[string]any{
				"engine":  inst.Engine,
				"major":   inst.Major,
				"state":   state,
				"port":    inst.Port,
				"datadir": inst.Datadir,
			}
			if running {
				row["version"] = instanceVersion(inst)
				row["rssBytes"] = rssForInstance(inst)
			}
			rows = append(rows, row)
		}
		b, _ := json.MarshalIndent(map[string]any{"instances": rows}, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	for _, inst := range instances {
		if err := showStatus(inst.Engine, inst.Major, false); err != nil {
			return err
		}
	}
	return nil
}

// instanceVersion returns the server version string for a running instance.
func instanceVersion(inst *Instance) string {
	if inst.Engine == "mysql" {
		return mysqlVersion(inst)
	}
	return postgresVersion(inst)
}

// postgresVersion queries the Postgres server version.
func postgresVersion(inst *Instance) string {
	pw := readPass(passPath(inst.Engine, inst.Major))
	connStr := buildURL(inst, "postgres", pw, false)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		return ""
	}
	defer conn.Close(ctx)
	// SHOW server_version returns the short form ("17.11"), unlike SELECT version()
	// which returns the long build string.
	var v string
	if err := conn.QueryRow(ctx, "SHOW server_version").Scan(&v); err != nil {
		return ""
	}
	return v
}

// CountClientBackends returns the number of client backends for an instance.
// Dispatches to the engine-specific implementation.
func CountClientBackends(inst *Instance) int {
	if inst.Engine == "mysql" {
		return countMySQLClientBackends(inst)
	}
	return countPGClientBackends(inst)
}

// ---- install ----

// ensureInstalled checks that <root>/bin/postgres exists; if not, runs
// workspace-agent install-postgres <major> to install it.
func ensureInstalled(root, major string) error {
	bin := filepath.Join(root, "bin", "postgres")
	if _, err := os.Stat(bin); err == nil {
		return nil
	}
	fmt.Fprintf(os.Stderr, "af-db: postgres-%s not installed; installing...\n", major)
	self, err := os.Executable()
	if err != nil {
		self = "/usr/local/bin/workspace-agent"
	}
	cmd := exec.Command(self, "install-postgres", major)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return errInstall(fmt.Sprintf("install-postgres %s failed: %v", major, err))
	}
	if _, err := os.Stat(bin); err != nil {
		return errInstall(fmt.Sprintf("install-postgres %s completed but %s not found", major, bin))
	}
	return nil
}

// ---- server lifecycle (Postgres) ----

// startServer runs initdb (if datadir absent), then pg_ctl start.
// Caller must hold the start lock.
func startServer(inst *Instance, persist bool) error {
	major := inst.Major
	root := inst.Root
	binDir := filepath.Join(root, "bin")

	var base string
	if persist {
		base = homeStateBase()
	} else {
		base = scratchBase()
	}
	datadir := filepath.Join(base, fmt.Sprintf("postgres-%s", major), "data")
	sockdir := filepath.Join(sockBase(), fmt.Sprintf("postgres-%s", major))
	logFile := filepath.Join(base, fmt.Sprintf("postgres-%s.log", major))

	inst.Datadir = datadir
	inst.Sockdir = sockdir
	inst.Persist = persist

	if _, err := os.Stat(datadir); os.IsNotExist(err) {
		if err := os.MkdirAll(datadir, 0o700); err != nil {
			return errStart(fmt.Sprintf("create datadir: %v", err))
		}
		if err := os.MkdirAll(sockdir, 0o700); err != nil {
			return errStart(fmt.Sprintf("create sockdir: %v", err))
		}

		pp := passPath("postgres", major)
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
func stopServer(inst *Instance) error {
	if inst.Datadir == "" {
		return nil
	}

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
		if !isPGRunning(livePID, inst.Datadir) {
			return nil
		}
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

// ---- database management via pgx (Postgres) ----

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

// reconcile drops databases whose recorded directory no longer exists on disk (Postgres).
func reconcile(inst *Instance, pw string) error {
	if !isPGRunning(inst.PID, inst.Datadir) || len(inst.Databases) == 0 {
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

// countPGClientBackends returns the number of client backends connected to the Postgres
// instance, excluding the monitoring connection itself. Returns -1 on any error.
func countPGClientBackends(inst *Instance) int {
	if !isPGRunning(inst.PID, inst.Datadir) {
		return -1
	}
	pw := readPass(passPath(inst.Engine, inst.Major))
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
