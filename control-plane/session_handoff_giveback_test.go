package main

// The recipient's give-back of a launch whose outcome stays unknown (issue #2058), through the
// CP's real routes on SQLite and, with AF_TEST_DATABASE_URL set, Postgres: only the recipient
// can give it back, only a take in the unknown state is released, and neither the sender's
// withdraw nor the recipient's own reconcile can win alongside it. A session the recipient's
// Agent reports after the give-back is shown to the recipient and moves nothing.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// handoffDialects runs f on a fresh recovery fixture per dialect.
func handoffDialects(t *testing.T, f func(t *testing.T, h *handoffRecovery)) {
	t.Run("sqlite", func(t *testing.T) { f(t, newHandoffRecovery(t)) })
	t.Run("postgres", func(t *testing.T) {
		url, ok := pgtest.Schema(t)
		if !ok {
			t.Skip("set AF_TEST_DATABASE_URL to run the Postgres half")
		}
		st, err := store.OpenPostgres(url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		f(t, newHandoffRecoveryIn(t, newInboxEnvOn(t, st)))
	})
}

func (h *handoffRecovery) giveBack(who string, body map[string]any) *httptest.ResponseRecorder {
	return h.do(who, http.MethodPost, "/api/session-handoff-offers/"+h.offer+"/give-back", body)
}

// confirmed is the give-back body the Console sends: the confirmation plus the takeRev the
// recipient's inbox shows for the offer (none when it lists no confirmed take).
func (h *handoffRecovery) confirmed() map[string]any {
	h.t.Helper()
	body := map[string]any{"confirmed": true}
	if rev, _ := h.inboxRow()["takeRev"].(string); rev != "" {
		body["takeRev"] = rev
	}
	return body
}

// inboxRow is the offer as the recipient's inbox lists it, nil when it is not listed.
func (h *handoffRecovery) inboxRow() map[string]any {
	h.t.Helper()
	rec := h.do("b", http.MethodGet, "/api/session-handoff-offers/received", nil)
	var out struct {
		Offers []map[string]any `json:"offers"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		h.t.Fatalf("inbox = %d %s", rec.Code, rec.Body.String())
	}
	for _, o := range out.Offers {
		if o["id"] == h.offer {
			return o
		}
	}
	return nil
}

// ageTake moves the take's confirm past the take window, which is what makes its outcome
// unknown rather than in progress.
func (h *handoffRecovery) ageTake() {
	h.t.Helper()
	old := time.Now().UTC().Add(-store.HandoffTakeWindow - time.Minute).Format(time.RFC3339)
	if _, err := h.st.DB().ExecContext(context.Background(), `UPDATE session_handoff_offer SET taken_at=?, take_confirmed_at=? WHERE id=? AND take_confirmed_at<>''`, old, old, h.offer); err != nil {
		h.t.Fatal(err)
	}
}

// requireTakeState asserts a 409 whose detail names the take's state.
func (h *handoffRecovery) requireTakeState(rec *httptest.ResponseRecorder, code, state string) {
	h.t.Helper()
	var out struct {
		Error struct {
			Code      string  `json:"code"`
			TakeState *string `json:"takeState"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusConflict || out.Error.Code != code || out.Error.TakeState == nil || *out.Error.TakeState != state {
		h.t.Fatalf("answer = %d %s, want 409 %s with takeState %q", rec.Code, rec.Body.String(), code, state)
	}
}

func (h *handoffRecovery) notices(who, kind string) []store.Notification {
	h.t.Helper()
	ns, err := h.st.ListNotifications(context.Background(), h.members[who], "", 100)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []store.Notification
	for _, n := range ns {
		if n.Kind == kind {
			out = append(out, n)
		}
	}
	return out
}

// listedTakeState reads the offer's takeState as the sender's ledger and the recipient's inbox
// show it.
func (h *handoffRecovery) listedTakeState(who, path string) string {
	h.t.Helper()
	rec := h.do(who, http.MethodGet, path, nil)
	var out struct {
		Offers []map[string]any `json:"offers"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		h.t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body.String())
	}
	for _, o := range out.Offers {
		if o["id"] == h.offer {
			s, _ := o["takeState"].(string)
			return s
		}
	}
	h.t.Fatalf("GET %s does not list the offer: %s", path, rec.Body.String())
	return ""
}

func TestHandoffGiveBackOnlyByTheRecipientOfAnUnknownTake(t *testing.T) {
	handoffDialects(t, func(t *testing.T, h *handoffRecovery) {
		h.agent.set("drop", "old") // nothing created, and an Agent that cannot say so
		token := h.mustTake()
		h.requireTakeState(h.giveBack("b", h.confirmed()), "handoff_not_unknown", "starting")
		h.requireTakeState(h.withdraw(), "handoff_being_taken", "starting")
		_ = h.create(token)
		rec, _ := h.take()
		h.requireCode(rec, http.StatusConflict, "handoff_launch_unknown")

		// Within the window the create may still be on its way: not given back.
		h.requireTakeState(h.giveBack("b", h.confirmed()), "handoff_not_unknown", "confirming")
		h.requireTakeState(h.withdraw(), "handoff_being_taken", "confirming")
		if s := h.listedTakeState("a", "/api/session-handoff-offers"); s != "confirming" {
			t.Fatalf("the sender's ledger says takeState %q, want confirming", s)
		}

		h.ageTake()
		if s := h.listedTakeState("a", "/api/session-handoff-offers"); s != "unknown" {
			t.Fatalf("the sender's ledger says takeState %q, want unknown", s)
		}
		if s := h.listedTakeState("b", "/api/session-handoff-offers/received"); s != "unknown" {
			t.Fatalf("the recipient's inbox says takeState %q, want unknown", s)
		}
		h.requireTakeState(h.withdraw(), "handoff_being_taken", "unknown")
		for _, who := range []string{"a", "c", "x"} { // the sender, a tenant peer, another tenant
			h.requireCode(h.giveBack(who, h.confirmed()), http.StatusNotFound, "not_found")
		}
		h.requireCode(h.giveBack("b", map[string]any{}), http.StatusBadRequest, "handoff_give_back_unconfirmed")
		if o := h.row(); o.TakeConfirmedAt == "" {
			t.Fatalf("a refused give-back moved the take: %+v", o)
		}

		rec = h.giveBack("b", h.confirmed())
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"pending"`) {
			t.Fatalf("give-back = %d %s, want 200 pending", rec.Code, rec.Body.String())
		}
		if o := h.row(); o.Status != "pending" || o.TakenAt != "" || o.TakeConfirmedAt != "" {
			t.Fatalf("after the give-back: %+v, want pending and untaken", o)
		}
		audits, err := h.st.ListAuditByTenant(context.Background(), h.t1.ID, 100)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, a := range audits {
			if a.Action == "session.handoff.give_back" {
				n++
				if a.Detail != "" || a.Target != "s1" {
					t.Fatalf("give-back audit %+v, want target s1 and no detail", a)
				}
			}
		}
		if n != 1 {
			t.Fatalf("%d give-back audit rows, want 1", n)
		}
		// The same take's create is fenced, and the offer is the sender's to withdraw again.
		h.agent.set("ok", "agent")
		h.requireCode(h.create(token), http.StatusConflict, "handoff_take_lost")
		h.requireCode(h.withdraw(), http.StatusOK, "")
		h.requireCode(h.giveBack("b", h.confirmed()), http.StatusConflict, "handoff_already_decided")
		if h.agent.count() != 0 {
			t.Fatalf("sessions=%d, want none", h.agent.count())
		}
	})
}

// TestHandoffGiveBackRacesTheWithdraw fires the sender's withdraw and the recipient's give-back
// at once on an unknown take, round after round: the withdraw may land only after the
// give-back, never past the take.
func TestHandoffGiveBackRacesTheWithdraw(t *testing.T) {
	handoffDialects(t, func(t *testing.T, h *handoffRecovery) {
		ctx := context.Background()
		base := h.row()
		old := time.Now().UTC().Add(-store.HandoffTakeWindow - time.Minute).Format(time.RFC3339)
		gaveBack, withdrew := 0, 0
		for i := 0; i < 20; i++ {
			if i > 0 {
				o := base
				o.ID, o.Status, o.CreatedAt = store.NewID(), "pending", store.NowTS()
				if created, err := h.st.CreateSessionHandoffOffer(ctx, o); err != nil || !created {
					t.Fatalf("round %d: offer created=%v err=%v", i, created, err)
				}
				h.offer = o.ID
			}
			if ok, err := h.st.TakeSessionHandoffOffer(ctx, h.offer, h.members["b"], "tok", old); err != nil || !ok {
				t.Fatalf("round %d: take=%v err=%v", i, ok, err)
			}
			if ok, err := h.st.ConfirmSessionHandoffTake(ctx, h.offer, h.members["b"], "tok", old); err != nil || !ok {
				t.Fatalf("round %d: confirm=%v err=%v", i, ok, err)
			}
			body := h.confirmed()
			if body["takeRev"] == nil {
				t.Fatalf("round %d: the inbox shows no takeRev for the confirmed take", i)
			}
			var gave, wd *httptest.ResponseRecorder
			var wg sync.WaitGroup
			start := make(chan struct{})
			wg.Add(2)
			go func() { defer wg.Done(); <-start; gave = h.giveBack("b", body) }()
			go func() { defer wg.Done(); <-start; wd = h.withdraw() }()
			close(start)
			wg.Wait()
			if gave.Code != http.StatusOK {
				t.Fatalf("round %d: give-back = %d %s; nothing else could have settled the take", i, gave.Code, gave.Body.String())
			}
			gaveBack++
			switch wd.Code {
			case http.StatusOK:
				withdrew++
			case http.StatusConflict:
				// Refused while the take held; the answer reads the row after the give-back may
				// have landed, so its takeState is "unknown" or "".
				h.requireCode(wd, http.StatusConflict, "handoff_being_taken")
				h.requireCode(h.withdraw(), http.StatusOK, "") // the sender's next try lands
			default:
				t.Fatalf("round %d: withdraw = %d %s", i, wd.Code, wd.Body.String())
			}
			if o := h.row(); o.Status != "withdrawn" {
				t.Fatalf("round %d: %+v, want withdrawn", i, o)
			}
		}
		t.Logf("%d give-backs, %d withdraws landed in the same round", gaveBack, withdrew)
	})
}

// holdAbandon makes the fake Agent's abandon wait until unblock is called, signalling arrived.
func (h *handoffRecovery) holdAbandon() (arrived chan struct{}, unblock func()) {
	inner := h.agent.srv.Config.Handler
	arrived, release := make(chan struct{}), make(chan struct{})
	var once, freed sync.Once
	unblock = func() { freed.Do(func() { close(release) }) }
	h.t.Cleanup(unblock)
	h.agent.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/abandon") {
			once.Do(func() { close(arrived) })
			<-release
		}
		inner.ServeHTTP(w, r)
	})
	return arrived, unblock
}

// TestHandoffGiveBackRacesTheReconcile: the recipient's next press asks their Agent about the
// lost create, and the give-back lands while it asks. The Agent then reports the session: the
// offer is not flipped to accepted behind the give-back, the press says so with the session's
// name, and the recipient is notified.
func TestHandoffGiveBackRacesTheReconcile(t *testing.T) {
	handoffDialects(t, func(t *testing.T, h *handoffRecovery) {
		h.agent.set("lost", "agent")
		_ = h.create(h.mustTake())
		h.ageTake()
		arrived, unblock := h.holdAbandon()
		press := make(chan *httptest.ResponseRecorder, 1)
		go func() { rec, _ := h.take(); press <- rec }()
		<-arrived
		gave := h.giveBack("b", h.confirmed())
		unblock()
		rec := <-press
		h.requireCode(gave, http.StatusOK, "")
		var out struct {
			Error struct {
				Code, SessionName, OfferStatus string
			} `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != http.StatusConflict || out.Error.Code != "handoff_session_after_give_back" || out.Error.SessionName != "s-1" || out.Error.OfferStatus != "pending" {
			t.Fatalf("the press = %d %s, want 409 handoff_session_after_give_back naming s-1", rec.Code, rec.Body.String())
		}
		if o := h.row(); o.Status != "pending" || o.TakenAt != "" {
			t.Fatalf("the late report moved the offer: %+v", o)
		}
		if n := h.notices("b", "handoff-late-session"); len(n) != 1 || n[0].TargetID != "s-1" || n[0].TargetType != "session" {
			t.Fatalf("the recipient's late-session notices: %+v, want one for s-1", n)
		}
		if h.acceptedNotices() != 0 {
			t.Fatal("the sender was told the offer was accepted")
		}
	})
}

// TestHandoffGiveBackAfterTheReconcileAccepted is the other order: the press found the session
// and accepted the offer, so the give-back finds it decided and changes nothing.
func TestHandoffGiveBackAfterTheReconcileAccepted(t *testing.T) {
	handoffDialects(t, func(t *testing.T, h *handoffRecovery) {
		h.agent.set("lost", "agent")
		_ = h.create(h.mustTake())
		h.ageTake()
		if rec, got := h.take(); rec.Code != http.StatusOK || got.Status != "accepted" {
			t.Fatalf("reconcile = %d %s", rec.Code, rec.Body.String())
		}
		h.requireCode(h.giveBack("b", h.confirmed()), http.StatusConflict, "handoff_already_decided")
		if o := h.row(); o.Status != "accepted" || o.AcceptedSessionName != "s-1" {
			t.Fatalf("the give-back moved an accepted offer: %+v", o)
		}
		if n := h.notices("b", "handoff-late-session"); len(n) != 0 {
			t.Fatalf("late-session notices after an ordinary reconcile: %+v", n)
		}
	})
}

// TestHandoffLateCreateAfterGiveBack: the create itself answers only after the recipient gave
// the take back and the sender withdrew. The CP does not accept the withdrawn offer behind the
// sender's back; the recipient is told a session started from it.
func TestHandoffLateCreateAfterGiveBack(t *testing.T) {
	handoffDialects(t, func(t *testing.T, h *handoffRecovery) {
		inner := h.agent.srv.Config.Handler
		arrived, release := make(chan struct{}), make(chan struct{})
		h.agent.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/sessions" {
				close(arrived)
				<-release
			}
			inner.ServeHTTP(w, r)
		})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		t.Cleanup(unblock) // a failed assertion must not leave the create holding the server open
		token := h.mustTake()
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- h.create(token) }()
		<-arrived
		h.ageTake() // the create has been running for longer than the window
		h.requireCode(h.giveBack("b", h.confirmed()), http.StatusOK, "")
		h.requireCode(h.withdraw(), http.StatusOK, "")
		unblock()
		if late := <-done; late.Code != http.StatusCreated {
			t.Fatalf("the late create = %d %s", late.Code, late.Body.String())
		}
		if o := h.row(); o.Status != "withdrawn" || o.AcceptedSessionName != "" {
			t.Fatalf("the late create settled the offer: %+v, want withdrawn", o)
		}
		n := h.notices("b", "handoff-late-session")
		if len(n) != 1 || n[0].TargetID != "s-1" || !strings.Contains(n[0].Payload, `"offerStatus":"withdrawn"`) {
			t.Fatalf("the recipient's late-session notices: %+v, want one for s-1 naming the withdrawn offer", n)
		}
		if h.acceptedNotices() != 0 {
			t.Fatal("the sender was told the offer was accepted")
		}
	})
}

// TestHandoffGiveBackRecoversAnUnsharedOrArchivedTake (review F1): a confirmed take outlives
// the inbox's filters. After an unshare or an archive the recipient's inbox still lists the
// offer as a recovery row — no body, no coordinates — whose give-back frees it: an unshared
// offer expires, an archived one is the sender's to withdraw again.
func TestHandoffGiveBackRecoversAnUnsharedOrArchivedTake(t *testing.T) {
	for _, how := range []string{"unshare", "archive"} {
		t.Run(how, func(t *testing.T) {
			handoffDialects(t, func(t *testing.T, h *handoffRecovery) {
				ctx := context.Background()
				h.agent.set("drop", "old")
				_ = h.create(h.mustTake())
				h.ageTake()
				if row := h.inboxRow(); row == nil || row["recoveryOnly"] != nil {
					t.Fatalf("before the %s the inbox row is %v, want an ordinary row", how, row)
				}
				switch how {
				case "unshare":
					if err := h.st.DeleteSessionShare(ctx, "share-1", h.members["a"]); err != nil {
						t.Fatal(err)
					}
				case "archive":
					cats, err := h.st.ListSharedSessionCatalogByOwner(ctx, h.members["a"])
					if err != nil || len(cats) != 1 {
						t.Fatalf("catalog = %v err=%v", cats, err)
					}
					cats[0].Archived = true
					if err := h.st.ReplaceSharedSessionCatalog(ctx, "ws-a", h.members["a"], cats); err != nil {
						t.Fatal(err)
					}
				}
				row := h.inboxRow()
				if row == nil || row["recoveryOnly"] != true || row["takeState"] != "unknown" || row["takeRev"] == nil ||
					row["prompt"] != "" || row["branch"] != "" || row["repoRemote"] != "" || row["headSha"] != "" {
					t.Fatalf("after the %s the inbox row is %v, want a recovery row with no body or coordinates", how, row)
				}
				h.requireTakeState(h.withdraw(), "handoff_being_taken", "unknown")
				rec := h.giveBack("b", h.confirmed())
				want := map[string]string{"unshare": "expired", "archive": "pending"}[how]
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"`+want+`"`) {
					t.Fatalf("give-back = %d %s, want 200 %s", rec.Code, rec.Body.String(), want)
				}
				if row := h.inboxRow(); row != nil {
					t.Fatalf("after the give-back the inbox still lists %v", row)
				}
				if how == "archive" {
					h.requireCode(h.withdraw(), http.StatusOK, "")
				}
			})
		})
	}
}

// TestHandoffGiveBackStaleConfirmation (review F2): a give-back confirmed against take A must
// not release take B, taken after A was given back, even once B's outcome is unknown too: an
// old tab or a delayed request carries A's takeRev.
func TestHandoffGiveBackStaleConfirmation(t *testing.T) {
	handoffDialects(t, func(t *testing.T, h *handoffRecovery) {
		h.agent.set("drop", "old")
		_ = h.create(h.mustTake())
		h.ageTake()
		stale := h.confirmed()
		h.requireCode(h.giveBack("b", stale), http.StatusOK, "")
		_ = h.create(h.mustTake())
		h.ageTake()
		h.requireTakeState(h.giveBack("b", stale), "handoff_give_back_stale", "unknown")
		h.requireCode(h.giveBack("b", map[string]any{"confirmed": true}), http.StatusConflict, "handoff_give_back_stale")
		if o := h.row(); o.TakeConfirmedAt == "" {
			t.Fatalf("a stale give-back released the later take: %+v", o)
		}
		h.requireTakeState(h.withdraw(), "handoff_being_taken", "unknown")
		h.requireCode(h.giveBack("b", h.confirmed()), http.StatusOK, "")
	})
}

// TestHandoffLateReportAfterTheSourceIsDeleted (review F4): after the give-back the sender
// deletes the source session, which deletes the offer with it. A session the recipient's Agent
// reports afterwards — to a reconcile already asking, or as the create's own late answer — is
// still named to the recipient; the offer is not restored.
func TestHandoffLateReportAfterTheSourceIsDeleted(t *testing.T) {
	deleteSource := func(t *testing.T, h *handoffRecovery) {
		t.Helper()
		if err := h.st.ReplaceSharedSessionCatalog(context.Background(), "ws-a", h.members["a"], nil); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := h.st.GetSessionHandoffOffer(context.Background(), h.offer); err != nil || ok {
			t.Fatalf("the offer survived its source: ok=%v err=%v", ok, err)
		}
	}
	t.Run("reconcile", func(t *testing.T) {
		handoffDialects(t, func(t *testing.T, h *handoffRecovery) {
			h.agent.set("lost", "agent")
			_ = h.create(h.mustTake())
			h.ageTake()
			arrived, unblock := h.holdAbandon()
			press := make(chan *httptest.ResponseRecorder, 1)
			go func() { rec, _ := h.take(); press <- rec }()
			<-arrived
			h.requireCode(h.giveBack("b", h.confirmed()), http.StatusOK, "")
			deleteSource(t, h)
			unblock()
			rec := <-press
			var out struct {
				Error struct{ Code, SessionName string } `json:"error"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			if rec.Code != http.StatusConflict || out.Error.Code != "handoff_session_after_give_back" || out.Error.SessionName != "s-1" {
				t.Fatalf("the press = %d %s, want handoff_session_after_give_back naming s-1", rec.Code, rec.Body.String())
			}
			if n := h.notices("b", "handoff-late-session"); len(n) != 1 || n[0].TargetID != "s-1" {
				t.Fatalf("the recipient's late-session notices: %+v, want one for s-1", n)
			}
			for _, who := range []string{"a", "c"} {
				if n := h.notices(who, "handoff-late-session"); len(n) != 0 {
					t.Fatalf("%s was told: %+v", who, n)
				}
			}
		})
	})
	t.Run("create", func(t *testing.T) {
		handoffDialects(t, func(t *testing.T, h *handoffRecovery) {
			inner := h.agent.srv.Config.Handler
			arrived, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			h.agent.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == "/sessions" {
					close(arrived)
					<-release
				}
				inner.ServeHTTP(w, r)
			})
			token := h.mustTake()
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- h.create(token) }()
			<-arrived
			h.ageTake()
			h.requireCode(h.giveBack("b", h.confirmed()), http.StatusOK, "")
			deleteSource(t, h)
			unblock()
			if late := <-done; late.Code != http.StatusCreated {
				t.Fatalf("the late create = %d %s", late.Code, late.Body.String())
			}
			n := h.notices("b", "handoff-late-session")
			if len(n) != 1 || n[0].TargetID != "s-1" || n[0].DisplayName != "carry on" || !strings.Contains(n[0].Payload, `"offerStatus":""`) {
				t.Fatalf("the recipient's late-session notices: %+v, want one for s-1 from the snapshot", n)
			}
		})
	})
}

// receivedHookStore runs hook once, right after the received list was read from the store and
// before the route derives anything from it: the gap a second read would race.
type receivedHookStore struct {
	store.Store
	once sync.Once
	hook func()
}

func (s *receivedHookStore) ListReceivedSessionHandoffOffers(ctx context.Context, membershipID string) ([]store.ReceivedHandoffOffer, error) {
	rows, err := s.Store.ListReceivedSessionHandoffOffers(ctx, membershipID)
	s.once.Do(s.hook)
	return rows, err
}

// TestHandoffReceivedRevisionNamesTheRowsTake (review F2-R2): the inbox row read under take A,
// with A given back and B confirmed (and gone unknown) before the answer is built, carries A's
// revision, so the give-back it leads to is refused as stale instead of releasing B.
func TestHandoffReceivedRevisionNamesTheRowsTake(t *testing.T) {
	handoffDialects(t, func(t *testing.T, h *handoffRecovery) {
		ctx := context.Background()
		h.agent.set("drop", "old")
		_ = h.create(h.mustTake())
		h.ageTake()
		old := time.Now().UTC().Add(-store.HandoffTakeWindow - time.Minute).Format(time.RFC3339)
		b := h.members["b"]
		h.mgr.store = &receivedHookStore{Store: h.st, hook: func() {
			a, held, err := h.st.ConfirmedSessionHandoffTake(ctx, h.offer, b)
			if err != nil || !held {
				t.Errorf("A: held=%v err=%v", held, err)
				return
			}
			if ok, err := h.st.GiveBackUnknownSessionHandoffTake(ctx, h.offer, b, a, time.Now()); err != nil || !ok {
				t.Errorf("give back A: ok=%v err=%v", ok, err)
			}
			if ok, err := h.st.TakeSessionHandoffOffer(ctx, h.offer, b, "B", old); err != nil || !ok {
				t.Errorf("take B: ok=%v err=%v", ok, err)
			}
			if ok, err := h.st.ConfirmSessionHandoffTake(ctx, h.offer, b, "B", old); err != nil || !ok {
				t.Errorf("confirm B: ok=%v err=%v", ok, err)
			}
		}}
		body := h.confirmed() // the received answer whose row was read under A
		if body["takeRev"] == nil || body["takeRev"] == handoffTakeRev("B") {
			t.Fatalf("the row read under A carries %v, want A's revision", body["takeRev"])
		}
		h.requireTakeState(h.giveBack("b", body), "handoff_give_back_stale", "unknown")
		if token, held, _ := h.st.ConfirmedSessionHandoffTake(ctx, h.offer, b); !held || token != "B" {
			t.Fatalf("B was released by a give-back confirmed against A: held=%v token=%q", held, token)
		}
		h.requireCode(h.giveBack("b", h.confirmed()), http.StatusOK, "")
	})
}

// TestHandoffRecoveryRowNeverLaunches (review F5): an archived offer whose take is confirmed is
// a recovery row; a Console that ignores recoveryOnly (an older tab) presses launch on it, the
// reconcile finds nothing was created and gives the take back, and no new take is handed out:
// nothing launches.
func TestHandoffRecoveryRowNeverLaunches(t *testing.T) {
	handoffDialects(t, func(t *testing.T, h *handoffRecovery) {
		ctx := context.Background()
		h.agent.set("drop", "agent") // nothing created, the key reserved: abandon answers not_created
		_ = h.create(h.mustTake())
		h.ageTake()
		cats, err := h.st.ListSharedSessionCatalogByOwner(ctx, h.members["a"])
		if err != nil || len(cats) != 1 {
			t.Fatalf("catalog = %v err=%v", cats, err)
		}
		cats[0].Archived = true
		if err := h.st.ReplaceSharedSessionCatalog(ctx, "ws-a", h.members["a"], cats); err != nil {
			t.Fatal(err)
		}
		if row := h.inboxRow(); row == nil || row["recoveryOnly"] != true || row["prompt"] != "" {
			t.Fatalf("the inbox row is %v, want a recovery row", row)
		}
		h.agent.set("ok", "agent")
		rec, out := h.take() // an old Console's press: body {}
		h.requireCode(rec, http.StatusConflict, "handoff_unavailable")
		if out.Token != "" {
			t.Fatalf("a recovery row yielded a launch token: %s", rec.Body.String())
		}
		if o := h.row(); o.Status != "pending" || o.TakenAt != "" {
			t.Fatalf("after the press: %+v, want pending and untaken", o)
		}
		if h.agent.count() != 0 {
			t.Fatalf("sessions=%d, want none", h.agent.count())
		}
		h.requireCode(h.withdraw(), http.StatusOK, "")
	})
}

// TestHandoffRecoveryRowDropsTheSpaceOrigin: a recovery row carries no body or coordinates, and
// its origin goes with them, so a Console never starts a new-space launch from one.
func TestHandoffRecoveryRowDropsTheSpaceOrigin(t *testing.T) {
	handoffDialects(t, func(t *testing.T, h *handoffRecovery) {
		ctx := context.Background()
		if _, err := h.st.DB().ExecContext(ctx, `UPDATE session_handoff_offer SET source_space=1 WHERE id=?`, h.offer); err != nil {
			t.Fatal(err)
		}
		h.agent.set("drop", "agent")
		_ = h.create(h.mustTake())
		h.ageTake()
		// While the session is listed the row is a normal one and says where it came from.
		if row := h.inboxRow(); row == nil || row["sourceSpace"] != true {
			t.Fatalf("the inbox row is %v, want a normal row with sourceSpace", row)
		}
		cats, err := h.st.ListSharedSessionCatalogByOwner(ctx, h.members["a"])
		if err != nil || len(cats) != 1 {
			t.Fatalf("catalog = %v err=%v", cats, err)
		}
		cats[0].Archived = true
		if err := h.st.ReplaceSharedSessionCatalog(ctx, "ws-a", h.members["a"], cats); err != nil {
			t.Fatal(err)
		}
		row := h.inboxRow()
		if row == nil || row["recoveryOnly"] != true {
			t.Fatalf("the inbox row is %v, want a recovery row", row)
		}
		if _, ok := row["sourceSpace"]; ok {
			t.Fatalf("a recovery row still carries sourceSpace: %v", row)
		}
	})
}
