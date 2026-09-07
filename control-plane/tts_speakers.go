// tts_speakers.go — the character catalogue that outlives the engine (ADR 0070
// decision 12).
//
// GET /api/tts/speakers used to be a plain proxy of the engine's /speakers, which was
// fine while the engine either existed and ran or did not exist at all. Under on-demand
// it is neither: stopped is the NORMAL state, so a proxy answers 502 almost always and
// the character picker — the one screen whose whole purpose is choosing a voice for
// later — is unusable exactly when somebody is setting it up.
//
// So the last catalogue the engine gave is kept in the same SettingsStore that already
// holds tts_engine and tts_dict, and answered from there while the engine is down. It is
// refreshed on every live read and, more importantly, by the controller the moment a
// fresh engine is warmed: without that, a deployment where nobody opens the settings
// screen during the engine's half hour of life would never capture one at all.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// ttsSpeakersSetting holds the catalogue as the JSON array the API returns.
const ttsSpeakersSetting = "tts_speakers"

// ttsSpeakersTTL keeps settings re-renders off both the engine and the store. The
// catalogue changes when the engine's version does, i.e. never within a minute.
const ttsSpeakersTTL = 60 * time.Second

type ttsSpeakerCache struct {
	settings store.SettingsStore // nil = nothing to persist to (tests, no store)

	mu    sync.Mutex
	list  []ttsSpeaker
	live  bool      // the cached copy came from the engine, not from the store
	at    time.Time // when the cached copy was taken
	saved string    // the JSON last written, so an unchanged catalogue costs no write
	now   func() time.Time
}

func (c *ttsSpeakerCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// get answers the catalogue and says whether it came from a live engine. engineUp is the
// caller's readiness probe, passed in rather than taken here so a stopped engine costs no
// HTTP attempt at all: the point of this cache is the deployment where that is the normal
// case.
func (c *ttsSpeakerCache) get(ctx context.Context, base string, engineUp bool) ([]ttsSpeaker, bool, *apiError) {
	c.mu.Lock()
	cached, live, at := c.list, c.live, c.at
	c.mu.Unlock()
	if len(cached) > 0 && !at.IsZero() && c.clock().Sub(at) < ttsSpeakersTTL {
		// A stored copy is not refreshed just because the engine came up mid-TTL: the
		// controller refreshes on warm-up, and a minute of an older catalogue changes
		// nothing a person can see.
		return cached, live, nil
	}
	if engineUp {
		if list, aerr := c.refresh(ctx, base); aerr == nil {
			return list, true, nil
		}
		// Falling through to the stored copy on purpose: an engine that is up but
		// answering badly is still no reason to take the picker away.
	}
	if list := c.stored(ctx); len(list) > 0 {
		c.mu.Lock()
		c.list, c.live, c.at = list, false, c.clock()
		c.mu.Unlock()
		return list, false, nil
	}
	return nil, false, &apiError{http.StatusBadGateway, "tts_engine_unreachable",
		"voicevox unreachable and no character catalogue has ever been stored"}
}

// refresh reads the live engine and records what it said. Called from the handler and,
// once per start, by the controller when a fresh engine has been warmed up.
func (c *ttsSpeakerCache) refresh(ctx context.Context, base string) ([]ttsSpeaker, *apiError) {
	list, aerr := voicevoxSpeakers(ctx, base)
	if aerr != nil {
		return nil, aerr
	}
	if len(list) == 0 {
		return nil, &apiError{http.StatusBadGateway, "tts_engine_error", "voicevox returned no speakers"}
	}
	c.mu.Lock()
	c.list, c.live, c.at = list, true, c.clock()
	prev := c.saved
	c.mu.Unlock()

	if c.settings == nil {
		return list, nil
	}
	b, err := json.Marshal(list)
	if err != nil || string(b) == prev {
		return list, nil
	}
	// Detached from the request's cancellation: the catalogue is worth keeping even if
	// the browser that happened to trigger this walked away mid-fetch.
	if err := c.settings.SetSetting(context.WithoutCancel(ctx), ttsSpeakersSetting, string(b)); err != nil {
		return list, nil // an unwritable store costs the durability, not the answer
	}
	c.mu.Lock()
	c.saved = string(b)
	c.mu.Unlock()
	return list, nil
}

// stored reads the durable copy. Anything unparseable is treated as absent: the catalogue
// is a cache, and a cache that cannot be read is simply a miss.
func (c *ttsSpeakerCache) stored(ctx context.Context) []ttsSpeaker {
	if c.settings == nil {
		return nil
	}
	v, err := c.settings.GetSetting(ctx, ttsSpeakersSetting)
	if err != nil || v == "" {
		return nil
	}
	var list []ttsSpeaker
	if json.Unmarshal([]byte(v), &list) != nil {
		return nil
	}
	return list
}
