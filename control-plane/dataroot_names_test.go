package main

// Tenant slugs, default-tenant user keys and the CP's own files share the
// directory directly under WS_DATA. These tests drive the admin and login paths that
// create a tenant or a default-tenant membership, the boot backfill that adopts homes,
// and the source and deploy scripts that name entries under the data root.

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/datalayout"
)

func apiErrCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var got struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	return got.Error.Code
}

func TestAdminAPIRefusesDataRootNameCollisions(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	def, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin")
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := st.UpsertIdentity(ctx, "alice@example.com", "alice-example-com", "")
	if _, err := st.EnsureMembership(ctx, alice.ID, def.ID, "member"); err != nil {
		t.Fatal(err)
	}
	adm := newAdminAPI(mgr)
	createTenant := func(slug string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/admin/tenants",
			strings.NewReader(`{"slug":"`+slug+`"}`))
		w := httptest.NewRecorder()
		adm.createTenant(w, r, admin)
		return w
	}
	for _, c := range []struct {
		slug   string
		status int
		code   string
	}{
		{"git", http.StatusBadRequest, "tenant_slug_reserved"},
		{"Shared", http.StatusBadRequest, "tenant_slug_reserved"}, // sanitized to "shared"
		{"alice@example.com", http.StatusConflict, "tenant_slug_conflict"},
		{"sales", http.StatusOK, ""},
	} {
		w := createTenant(c.slug)
		if w.Code != c.status || apiErrCode(t, w) != c.code {
			t.Errorf("create tenant %q = %d %s, want %d %q", c.slug, w.Code, w.Body.String(), c.status, c.code)
		}
	}

	invite := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/admin/memberships", strings.NewReader(body))
		r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
		w := httptest.NewRecorder()
		adm.addMembership(w, r)
		return w
	}
	for _, c := range []struct {
		body   string
		status int
		code   string
	}{
		{`{"tenant_slug":"default","user_key":"sales"}`, http.StatusConflict, "user_key_conflict"},
		{`{"tenant_slug":"default","email":"SALES"}`, http.StatusConflict, "user_key_conflict"},
		{`{"tenant_slug":"default","user_key":"git"}`, http.StatusConflict, "user_key_reserved"},
		// In another tenant the home nests under the slug, so the same key is fine.
		{`{"tenant_slug":"sales","user_key":"git"}`, http.StatusOK, ""},
		{`{"tenant_slug":"default","email":"bob@example.com"}`, http.StatusOK, ""},
	} {
		w := invite(c.body)
		if w.Code != c.status || apiErrCode(t, w) != c.code {
			t.Errorf("invite %s = %d %s, want %d %q", c.body, w.Code, w.Body.String(), c.status, c.code)
		}
	}
}

// AF_PROVISION=auto puts a first-time visitor into the default tenant; a visitor whose
// key is a tenant's directory gets a stated refusal instead of a home inside it.
func TestAutoProvisionRefusesAKeyThatIsATenantDirectory(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	mgr.provisionMode = "auto"
	if _, err := st.CreateTenant(ctx, "sales", "Sales"); err != nil {
		t.Fatal(err)
	}
	ident, err := st.UpsertIdentity(ctx, "", "sales", "")
	if err != nil {
		t.Fatal(err)
	}
	_, aerr := mgr.membershipsFor(ctx, ident)
	if aerr == nil || aerr.status != http.StatusConflict || aerr.code != errCodeUserKeyConflict {
		t.Fatalf("membershipsFor = %+v, want 409 %s", aerr, errCodeUserKeyConflict)
	}
}

// The boot backfill adopts every <root>/<key>/home as a default-tenant member. A
// directory that is a tenant's or a reserved name is skipped with a log line; the boot
// goes on and the real members are still adopted.
func TestBackfillSkipsDataRootNameCollisions(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	mgr.dataRoot = t.TempDir()
	if _, err := st.CreateTenant(ctx, "sales", "Sales"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"git", "sales", "alice"} {
		if err := os.MkdirAll(filepath.Join(mgr.dataRoot, d, "home"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := mgr.backfill(ctx); err != nil {
		t.Fatalf("backfill must not fail on a colliding directory: %v", err)
	}
	for key, want := range map[string]bool{"alice": true, "git": false, "sales": false} {
		ident, ok, err := st.GetIdentityByUserKey(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		member := false
		if ok {
			_, member, _ = st.GetMembership(ctx, ident.ID, mgr.defaultTenantID)
		}
		if member != want {
			t.Errorf("%s adopted as a default-tenant member = %v, want %v", key, member, want)
		}
	}
}

// dataRootDynamicJoins are the only places allowed to join a non-constant name directly
// onto the data root, keyed file:function:identifier. They are the homes and tenant
// directories the slug/key check exists for; anything else must be a datalayout constant.
var dataRootDynamicJoins = map[string]bool{
	"manager.go:workspaceNames:key":       true,
	"manager.go:workspaceNames:slug":      true,
	"workspace_lifecycle.go:backfill:key": true,
}

// dataRootJoinViolations scans one file for filepath.Join calls whose first argument is
// the data root (it mentions dataRoot or "WS_DATA"). The second argument must be a
// datalayout constant that is in Reserved, or an allow-listed dynamic join; a literal, a
// local or another package's constant, or any other expression is reported. It returns
// the violations and the datalayout constants it saw.
func dataRootJoinViolations(fset *token.FileSet, path string, src []byte) (bad []string, seen []string, err error) {
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return nil, nil, err
	}
	reserved := map[string]bool{}
	for _, r := range datalayout.Reserved() {
		reserved[r] = true
	}
	constVal := map[string]string{
		"GitDir": datalayout.GitDir, "DBFile": datalayout.DBFile,
		"GitTokenMasterFile": datalayout.GitTokenMasterFile, "DrawioStencilsDir": datalayout.DrawioStencilsDir,
	}
	text := func(e ast.Expr) string {
		return string(src[fset.Position(e.Pos()).Offset:fset.Position(e.End()).Offset])
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 || call.Ellipsis.IsValid() {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Join" {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "filepath" {
				return true
			}
			first := text(call.Args[0])
			if !strings.Contains(first, "dataRoot") && !strings.Contains(first, `"WS_DATA"`) {
				return true
			}
			at := fset.Position(call.Pos())
			arg := call.Args[1]
			if a, ok := arg.(*ast.SelectorExpr); ok {
				if x, ok := a.X.(*ast.Ident); ok && x.Name == "datalayout" {
					v, known := constVal[a.Sel.Name]
					switch {
					case !known:
						bad = append(bad, fmt.Sprintf("%s: datalayout.%s is not checked here; add it to constVal", at, a.Sel.Name))
					case !reserved[v]:
						bad = append(bad, fmt.Sprintf("%s: datalayout.%s (%q) is not in Reserved()", at, a.Sel.Name, v))
					}
					seen = append(seen, a.Sel.Name)
					return true
				}
			}
			if id, ok := arg.(*ast.Ident); ok && dataRootDynamicJoins[filepath.Base(path)+":"+fn.Name.Name+":"+id.Name] {
				return true
			}
			bad = append(bad, fmt.Sprintf("%s: %s joins %s onto the data root; name it in internal/datalayout (or allow-list a dynamic home/tenant join)", at, first, text(arg)))
			return true
		})
	}
	return bad, seen, nil
}

// Every entry the CP code creates directly under the data root must be a datalayout
// constant that is in Reserved; any other name is one the slug check cannot know about.
func TestDataRootJoinsUseReservedNames(t *testing.T) {
	fset := token.NewFileSet()
	seen := map[string]bool{}
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "testdata" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		bad, names, err := dataRootJoinViolations(fset, path, src)
		if err != nil {
			return err
		}
		for _, b := range bad {
			t.Error(b)
		}
		for _, n := range names {
			seen[n] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Positive control: the walk must have found the writers that exist today.
	for _, name := range []string{"GitDir", "DBFile", "GitTokenMasterFile", "DrawioStencilsDir"} {
		if !seen[name] {
			t.Errorf("no filepath.Join(dataRoot, datalayout.%s) found; the scan has stopped seeing the writers", name)
		}
	}
}

// The scanner itself: each way of smuggling a new top-level name past the list is caught,
// and only the allow-listed dynamic joins pass.
func TestDataRootJoinViolationsScanner(t *testing.T) {
	src := `package main

import (
	"path/filepath"
	"other"
	"github.com/k-k1/agent-fleet/control-plane/internal/datalayout"
)

const localName = "unreserved"

func workspaceNames(slug, key string) {
	_ = filepath.Join(m.dataRoot, key)
	_ = filepath.Join(m.dataRoot, slug, key)
}

func elsewhere(key string) {
	_ = filepath.Join(m.dataRoot, datalayout.GitDir)          // ok
	_ = filepath.Join(m.dataRoot, "literal")                  // 1
	_ = filepath.Join(m.dataRoot, localName)                  // 2
	_ = filepath.Join(m.dataRoot, other.Name)                 // 3
	_ = filepath.Join(m.dataRoot, key)                        // 4: not allow-listed here
	_ = filepath.Join(m.dataRoot, "a"+"b")                    // 5
	_ = filepath.Join(envx.Or("WS_DATA", "/x"), "drawio")      // 6
	_ = filepath.Join(datalayout.Unknown)                     // not a data-root join
	_ = filepath.Join(m.dataRoot, datalayout.Unknown)         // 7
	_ = filepath.Join(somewhereElse, "literal")               // not the data root
}
`
	bad, seen, err := dataRootJoinViolations(token.NewFileSet(), "manager.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 7 {
		t.Errorf("violations = %d, want 7:\n%s", len(bad), strings.Join(bad, "\n"))
	}
	for _, want := range []string{`"literal"`, "localName", "other.Name", ":21:", `"a"+"b"`, `"drawio"`, "datalayout.Unknown"} {
		if !strings.Contains(strings.Join(bad, "\n"), want) {
			t.Errorf("no violation mentions %s:\n%s", want, strings.Join(bad, "\n"))
		}
	}
	if len(seen) != 2 {
		t.Errorf("seen = %v, want GitDir and Unknown", seen)
	}
}

// The deployment scripts place their own directories under the data root too.
func TestDeployDataRootNamesAreReserved(t *testing.T) {
	re := regexp.MustCompile(`\$\{?(?:WS_DATA|DATA_DIR)\}?/([A-Za-z0-9._+-]+)`)
	found := map[string]bool{}
	err := filepath.WalkDir(filepath.Join("..", "deploy"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			if !datalayout.IsReserved(m[1]) && !found[m[1]] {
				t.Errorf("%s places %q under the data root, and it is not in datalayout.Reserved()", path, m[1])
			}
			found[m[1]] = true
		}
		return nil
	})
	if os.IsNotExist(err) {
		t.Skipf("deploy/ not available (%v)", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	// Positive control: compose names the DB under DATA_DIR today.
	if !found[datalayout.DBFile] || !found["shared"] {
		t.Fatalf("the scan found %v; it should at least see control-plane.db and shared", found)
	}
}
