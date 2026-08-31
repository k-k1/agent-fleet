package bridge

// Discord Gateway client (docs/log/37 P2a) — the receive half of the bridge. A single
// long-lived WSS connection (gorilla/websocket) that the receiver supervisor owns:
// it identifies with the minimal intents, keeps the connection alive with
// heartbeats, and hands each MESSAGE_CREATE to a callback. There is no existing
// Gateway precedent in the repo, so the heartbeat / RESUME / reconnect state
// machine is written here from the protocol (the WS plumbing mirrors codex's
// appclient: one reader goroutine, writes serialized behind a mutex).
//
// Send stays REST-only (discord.go). This file only READS.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Discord Gateway opcodes (only the ones we act on).
const (
	opDispatch     = 0
	opHeartbeat    = 1
	opIdentify     = 2
	opResume       = 6
	opReconnect    = 7
	opInvalidSess  = 9
	opHello        = 10
	opHeartbeatAck = 11
)

// discordIntents = GUILD_MESSAGES (1<<9) | MESSAGE_CONTENT (1<<15). A reply in a
// session's thread arrives as a guild MESSAGE_CREATE; MESSAGE_CONTENT (a privileged
// intent, one checkbox in the Developer Portal for bots in <100 guilds) is what makes
// the reply text readable — without it content is blank and P2a can't route (docs/log/37).
const discordIntents = (1 << 9) | (1 << 15) // 33280

// errDisallowedIntent is the fatal Gateway close (4014): the bot tried to identify
// with MESSAGE_CONTENT but it isn't enabled. The supervisor surfaces this instead of
// hammering reconnects — the user must flip the intent in the Developer Portal.
var errDisallowedIntent = errors.New("discord gateway: disallowed intents — enable MESSAGE_CONTENT for the bot")

// errAuthFailed is the fatal Gateway close (4004): a bad/rotated token.
var errAuthFailed = errors.New("discord gateway: authentication failed (bad token)")

// gatewayDialURL resolves the base WSS url to dial for a FRESH connection. A var so
// tests point it at a fake gateway server (same indirection as discordAPIBase).
var gatewayDialURL = func(token string) (string, error) {
	var res struct {
		URL string `json:"url"`
	}
	if err := discordDo("GET", "/gateway/bot", token, nil, &res); err != nil {
		return "", err
	}
	if res.URL == "" {
		return "", fmt.Errorf("discord: empty gateway url")
	}
	return res.URL, nil
}

// gatewayHandshakeTimeout bounds the WS dial.
var gatewayHandshakeTimeout = 10 * time.Second

// gwPayload is the Gateway frame envelope.
type gwPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
	S  *int            `json:"s,omitempty"` // sequence number (dispatch frames)
	T  string          `json:"t,omitempty"` // event name (dispatch frames)
}

// gatewayMessage is the slice of a MESSAGE_CREATE we need: who sent it, where, and
// what. Everything else in the Discord message object is ignored.
type gatewayMessage struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	GuildID   string `json:"guild_id"`
	Content   string `json:"content"`
	Author    struct {
		ID  string `json:"id"`
		Bot bool   `json:"bot"`
	} `json:"author"`
}

// gatewayInteraction is the slice of an INTERACTION_CREATE we need for P2b button
// clicks: who clicked (member.user in a guild, user in a DM), which button
// (data.custom_id), and where (channel + message) to acknowledge/edit. Only
// MESSAGE_COMPONENT (type 3) interactions are acted on.
type gatewayInteraction struct {
	ID        string `json:"id"`
	Type      int    `json:"type"`
	Token     string `json:"token"`
	ChannelID string `json:"channel_id"`
	Data      struct {
		CustomID string `json:"custom_id"`
	} `json:"data"`
	Message struct {
		ID string `json:"id"`
	} `json:"message"`
	Member struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	} `json:"member"`
	User struct {
		ID string `json:"id"`
	} `json:"user"`
}

// interactionTypeComponent is Discord's MESSAGE_COMPONENT interaction type.
const interactionTypeComponent = 3

// authorID is the clicker's user id (member.user in a guild, top-level user in a DM).
func (gi gatewayInteraction) authorID() string {
	if gi.Member.User.ID != "" {
		return gi.Member.User.ID
	}
	return gi.User.ID
}

// gateway holds one connection's worth of state plus the resume tokens carried across
// reconnects within a supervisor loop. Not safe for concurrent connectOnce calls — the
// supervisor runs them sequentially.
type gateway struct {
	token      string
	onMsg      func(gatewayMessage)
	onInteract func(gatewayInteraction)

	// resume state (persists across reconnects; cleared on a non-resumable invalid session)
	sessionID string
	resumeURL string
	seq       int
	haveSeq   bool

	// per-connection
	conn  *websocket.Conn
	wmu   sync.Mutex // serializes writes (gorilla constraint)
	acked bool       // last heartbeat was ACKed (zombie-connection guard)
	ackMu sync.Mutex
}

// connectOnce dials, handshakes, and pumps events until the connection drops or ctx is
// cancelled. It returns when the connection ends; the supervisor decides whether to
// reconnect. A fatal config error (errDisallowedIntent / errAuthFailed) is returned so
// the supervisor can stop instead of looping.
func (g *gateway) connectOnce(ctx context.Context) error {
	// Resume against resume_gateway_url when we have session state; otherwise a fresh
	// dial + IDENTIFY.
	resuming := g.sessionID != "" && g.resumeURL != ""
	base := g.resumeURL
	if !resuming {
		u, err := gatewayDialURL(g.token)
		if err != nil {
			return err
		}
		base = u
	}

	dialer := websocket.Dialer{HandshakeTimeout: gatewayHandshakeTimeout}
	conn, _, err := dialer.Dial(base+"/?v=10&encoding=json", http.Header{})
	if err != nil {
		return err
	}
	g.conn = conn
	defer conn.Close()

	// Close the conn when ctx is cancelled so the blocking ReadMessage below returns.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()

	// HELLO must arrive first; it carries the heartbeat interval.
	var hello struct {
		HeartbeatInterval int `json:"heartbeat_interval"`
	}
	first, err := g.readFrame()
	if err != nil {
		return g.classifyClose(err)
	}
	if first.Op != opHello {
		return fmt.Errorf("discord gateway: expected HELLO, got op %d", first.Op)
	}
	if err := json.Unmarshal(first.D, &hello); err != nil || hello.HeartbeatInterval <= 0 {
		return fmt.Errorf("discord gateway: bad HELLO")
	}

	g.setAcked(true)
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	// conn is passed by value: a late-exiting loop from a previous connection must
	// never read g.conn (already re-assigned) and close the NEW connection.
	go g.heartbeatLoop(hbCtx, conn, time.Duration(hello.HeartbeatInterval)*time.Millisecond)

	if resuming {
		if err := g.sendResume(); err != nil {
			return err
		}
	} else if err := g.sendIdentify(); err != nil {
		return err
	}

	for {
		p, err := g.readFrame()
		if err != nil {
			return g.classifyClose(err)
		}
		if p.S != nil {
			g.seq = *p.S
			g.haveSeq = true
		}
		switch p.Op {
		case opDispatch:
			g.onDispatch(p)
		case opHeartbeat:
			// Server asked for an immediate beat.
			_ = g.sendHeartbeat(conn)
		case opHeartbeatAck:
			g.setAcked(true)
		case opReconnect:
			// Server wants us to reconnect and RESUME — keep session state, return.
			return nil
		case opInvalidSess:
			// d is a bool: resumable? If not, drop session state so the supervisor
			// reconnects with a fresh IDENTIFY.
			var resumable bool
			_ = json.Unmarshal(p.D, &resumable)
			if !resumable {
				g.sessionID, g.resumeURL, g.haveSeq = "", "", false
			}
			return nil
		}
	}
}

// onDispatch handles op0 events. READY captures the resume tokens; MESSAGE_CREATE is
// the payload P2a cares about.
func (g *gateway) onDispatch(p gwPayload) {
	switch p.T {
	case "READY":
		var ready struct {
			SessionID string `json:"session_id"`
			ResumeURL string `json:"resume_gateway_url"`
		}
		if err := json.Unmarshal(p.D, &ready); err == nil {
			g.sessionID = ready.SessionID
			if ready.ResumeURL != "" {
				g.resumeURL = ready.ResumeURL
			}
		}
	case "RESUMED":
		// Nothing to do — the missed events (if any) replay as normal dispatches.
	case "MESSAGE_CREATE":
		var m gatewayMessage
		if err := json.Unmarshal(p.D, &m); err != nil {
			return
		}
		if g.onMsg != nil {
			g.onMsg(m)
		}
	case "INTERACTION_CREATE":
		// P2b: a button click. Interactions arrive over the Gateway when no public
		// Interactions Endpoint URL is set — exactly the local-only deployment.
		var gi gatewayInteraction
		if err := json.Unmarshal(p.D, &gi); err != nil {
			return
		}
		if gi.Type != interactionTypeComponent {
			return // ignore slash commands / autocomplete / modals
		}
		if g.onInteract != nil {
			g.onInteract(gi)
		}
	}
}

func (g *gateway) sendIdentify() error {
	return g.writeJSON(map[string]any{
		"op": opIdentify,
		"d": map[string]any{
			"token":   g.token,
			"intents": discordIntents,
			"properties": map[string]string{
				"os": "linux", "browser": "agent-fleet", "device": "agent-fleet",
			},
		},
	})
}

func (g *gateway) sendResume() error {
	return g.writeJSON(map[string]any{
		"op": opResume,
		"d": map[string]any{
			"token":      g.token,
			"session_id": g.sessionID,
			"seq":        g.seq,
		},
	})
}

// heartbeatLoop sends op1 at the interval. The first beat is jittered per the protocol
// (spread load across the fleet). A missing ACK between beats means a zombie connection
// — close the conn so connectOnce returns and the supervisor reconnects+resumes.
func (g *gateway) heartbeatLoop(ctx context.Context, conn *websocket.Conn, interval time.Duration) {
	// Jitter the first beat by a fraction of the interval (deterministic — no rand in
	// this environment; a fixed 1/2 is within spec).
	select {
	case <-time.After(interval / 2):
	case <-ctx.Done():
		return
	}
	for {
		if !g.isAcked() {
			_ = conn.Close() // zombie — force a reconnect
			return
		}
		g.setAcked(false)
		if err := g.sendHeartbeat(conn); err != nil {
			return
		}
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return
		}
	}
}

// sendHeartbeat takes the connection explicitly (not g.conn) because it is also
// called from the heartbeat goroutine, which may outlive this connection slightly.
func (g *gateway) sendHeartbeat(conn *websocket.Conn) error {
	var d any
	if g.haveSeq {
		d = g.seq
	}
	g.wmu.Lock()
	defer g.wmu.Unlock()
	return conn.WriteJSON(map[string]any{"op": opHeartbeat, "d": d})
}

func (g *gateway) readFrame() (gwPayload, error) {
	var p gwPayload
	_, b, err := g.conn.ReadMessage()
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("discord gateway: bad frame: %w", err)
	}
	return p, nil
}

func (g *gateway) writeJSON(v any) error {
	g.wmu.Lock()
	defer g.wmu.Unlock()
	return g.conn.WriteJSON(v)
}

func (g *gateway) setAcked(v bool) { g.ackMu.Lock(); g.acked = v; g.ackMu.Unlock() }
func (g *gateway) isAcked() bool   { g.ackMu.Lock(); defer g.ackMu.Unlock(); return g.acked }

// classifyClose turns a read error into either a fatal config error (so the supervisor
// stops) or a transient error (so it reconnects). A normal close / EOF is transient.
func (g *gateway) classifyClose(err error) error {
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		switch ce.Code {
		case 4014, 4013: // disallowed / invalid intents
			return errDisallowedIntent
		case 4004: // authentication failed
			return errAuthFailed
		}
	}
	return err
}
