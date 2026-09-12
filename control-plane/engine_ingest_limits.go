package main

// What a source says you may NOT do with a model, decided before anything is downloaded.
//
// The panel used to learn this from a refusal: the ingest job ran for minutes and the Fargate
// task died with `curl: (22) … error: 401`, or the row landed in the catalogue and the licence
// turned out to forbid the thing the deployment offers it for. Both are visible in the search
// answer — one of them only to a probe — so they are read here, on the list, where choosing
// something else is still free.
//
// Codes, not sentences: Hugging Face and Civitai spell the same restriction differently and the
// locale catalogue lives in the Console.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// The licence matrix Civitai publishes per model. Each is the ABSENCE of a permission the
	// uploader could have granted, which is why they are worth a tag: the default reading of a
	// model on a "free models" site is that it may be used for anything.
	engineRestrictNonCommercial = "noncommercial"  // allowCommercialUse is empty
	engineRestrictCredit        = "credit"         // allowNoCredit=false — attribution required
	engineRestrictNoDerivatives = "no_derivatives" // allowDerivatives=false
	engineRestrictSameLicense   = "same_license"   // allowDifferentLicense=false
	engineRestrictPaid          = "paid"           // behind a paid membership, permanently
	engineRestrictEarlyAccess   = "early_access"   // paid until a date, free after it
	engineRestrictPrivate       = "private"        // availability is not Public
	engineRestrictGenerateOnly  = "generate_only"  // usageControl != Download: on-site use only
	engineRestrictNSFW          = "nsfw"           // the uploader's own flag
	engineRestrictPOI           = "poi"            // depicts a real person
	engineRestrictMinor         = "minor"          // Civitai's own minor flag
	engineRestrictUnscanned     = "unscanned"      // the virus scan did not come back clean
	engineRestrictPickle        = "pickle"         // a pickle import — a .ckpt that runs code
	engineRestrictGatedAuto     = "gated_auto"     // HF: accept the terms with the token's account
	engineRestrictGatedManual   = "gated_manual"   // HF: the author approves each account by hand
)

// engineLoginYes / engineLoginNo are the two answers the probe can actually give. The third
// state is the empty string, and it is not a synonym for "no" — see engineSearchHit.
const (
	engineLoginYes = "yes"
	engineLoginNo  = "no"
)

// engineCivitaiLicenceFacts is the part of a Civitai model row that says what may be done with
// it. Decoded into its own struct so the two call sites (the search list and the resolve) read
// the same fields out of the same names.
type engineCivitaiLicenceFacts struct {
	AllowCommercialUse    []string `json:"allowCommercialUse"`
	AllowNoCredit         bool     `json:"allowNoCredit"`
	AllowDerivatives      bool     `json:"allowDerivatives"`
	AllowDifferentLicense bool     `json:"allowDifferentLicense"`
	Availability          string   `json:"availability"`
	NSFW                  bool     `json:"nsfw"`
	POI                   bool     `json:"poi"`
	Minor                 bool     `json:"minor"`
	HasActivePaidAccess   bool     `json:"hasActivePaidAccess"`
	// 🔴 known is set by whoever decoded this, and it gates the four permission flags below
	// the comment on engineCivitaiRestrictions. Every one of them is a NEGATIVE: false means
	// "not permitted", so a document that was never read — the model GET the resolve makes is
	// best-effort — would otherwise announce four restrictions that nobody published. Not a
	// JSON field: no answer contains it, and an upstream that invented one must not set it.
	known bool
}

// with marks the facts as actually read. Used at the two points where the decode succeeded.
func (f engineCivitaiLicenceFacts) with(known bool) engineCivitaiLicenceFacts {
	f.known = known
	return f
}

// engineCivitaiVersionFacts is the same question asked of one VERSION. `paidAccess` is an object
// or null, and 🔴 its `permanent` is the difference between "this costs money" and "this is free
// in a fortnight" — measured 2026-09-12, both shapes appear in the top 20.
type engineCivitaiVersionFacts struct {
	Availability string `json:"availability"`
	PaidAccess   *struct {
		Permanent bool   `json:"permanent"`
		EndsAt    string `json:"endsAt"`
	} `json:"paidAccess"`
	// UsageControl is "Download" for something that may be fetched at all. Anything else is a
	// model Civitai will only run on its own site, and taking it in is not on offer. 🔴 It is
	// NOT in `/api/v1/models` (measured 2026-09-12) — only in the per-version document — so
	// the search list leaves it empty and the resolve is where it bites.
	UsageControl string `json:"usageControl"`
}

// engineCivitaiFileFacts is what the scanners said about one file. A `.ckpt` is a pickle, which
// is code that runs on the GPU box this deployment pays for; "scanned and clean" is the only
// reason to treat one as ordinary.
type engineCivitaiFileFacts struct {
	PickleScanResult string `json:"pickleScanResult"`
	VirusScanResult  string `json:"virusScanResult"`
}

// engineCivitaiRestrictions collects every code the three answers above justify.
//
// Order is deliberate and is the order the panel draws: what stops the download, then what
// limits the use, then what the file is. Absent facts add nothing — a field a page does not
// publish must not become a reassuring tag.
func engineCivitaiRestrictions(m engineCivitaiLicenceFacts, v engineCivitaiVersionFacts, files []engineCivitaiFileFacts) []string {
	var out []string
	add := func(code string) { out = append(out, code) }
	switch {
	case v.PaidAccess != nil && v.PaidAccess.Permanent:
		add(engineRestrictPaid)
	case v.PaidAccess != nil:
		// Not permanent: a window that ends. The date rides in the panel's own tag text only if
		// somebody asks for it later — the code alone already answers "why can I not have it".
		add(engineRestrictEarlyAccess)
	case m.HasActivePaidAccess:
		// The MODEL has a paid version somewhere and this one says nothing. Measured: a row can
		// carry this while the version behind it downloads anonymously, so it is the weaker
		// claim and is only made when the version itself is silent.
		add(engineRestrictEarlyAccess)
	}
	if a := strings.TrimSpace(v.Availability); a != "" && !strings.EqualFold(a, "Public") {
		add(engineRestrictPrivate)
	} else if a := strings.TrimSpace(m.Availability); a != "" && !strings.EqualFold(a, "Public") {
		add(engineRestrictPrivate)
	}
	if u := strings.TrimSpace(v.UsageControl); u != "" && !strings.EqualFold(u, "Download") {
		add(engineRestrictGenerateOnly)
	}
	// The four permission flags, and ONLY when the document they come from was read. Each of
	// them is restrictive when false, which is also what an unread struct says.
	if m.known {
		if len(m.AllowCommercialUse) == 0 {
			add(engineRestrictNonCommercial)
		}
		if !m.AllowNoCredit {
			add(engineRestrictCredit)
		}
		if !m.AllowDerivatives {
			add(engineRestrictNoDerivatives)
		}
		if !m.AllowDifferentLicense {
			add(engineRestrictSameLicense)
		}
	}
	if m.NSFW {
		add(engineRestrictNSFW)
	}
	if m.POI {
		add(engineRestrictPOI)
	}
	if m.Minor {
		add(engineRestrictMinor)
	}
	for _, f := range files {
		if r := strings.TrimSpace(f.VirusScanResult); r != "" && !strings.EqualFold(r, "Success") {
			add(engineRestrictUnscanned)
			break
		}
	}
	for _, f := range files {
		if r := strings.TrimSpace(f.PickleScanResult); r != "" && !strings.EqualFold(r, "Success") {
			add(engineRestrictPickle)
			break
		}
	}
	return out
}

// engineHFGatedKind reads WHICH gate, out of the same field engineHFGated reads as a bool.
// "" for a repository that is not gated; the two strings are Hugging Face's own.
func engineHFGatedKind(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "auto":
		return "auto"
	case "manual":
		return "manual"
	}
	return ""
}

// engineHFRestrictions turns the gate into the panel's vocabulary. A gated repository whose kind
// the API did not name still gets the weaker of the two: "somebody has to accept something" is
// the part that is certain.
func engineHFRestrictions(gated any) []string {
	if !engineHFGated(gated) {
		return nil
	}
	if engineHFGatedKind(gated) == "manual" {
		return []string{engineRestrictGatedManual}
	}
	return []string{engineRestrictGatedAuto}
}

// engineLoginProbeWorkers is how many HEADs are in flight at once. The list is 20 rows and each
// probe is one round trip, so this is the difference between a search that answers in about a
// second and one that answers in twenty.
const engineLoginProbeWorkers = 8

// engineProbeCivitaiLogins fills LoginRequired for a whole page of hits.
//
// 🔴 This exists because NOTHING in the metadata predicts the answer (see
// engineSearchHit.LoginRequired for the measurement), and because 13 of 20 is not an edge case —
// it is the majority of what Civitai's own ranking puts on the first screen. Learning it from a
// nine-minute ingest that ends in `401` is the dead end the whole picker exists to avoid.
//
// It is STRICTER than engineCivitaiAnonymous, which the resolve uses: that one fails open in
// every direction because it must not block an ingest that would have worked, and answering
// "no" is its fail-open. Here a probe that could not run leaves the field empty, because this
// answer is drawn as a fact next to twenty others and a rate-limited 429 read as "anyone may
// download this" is a worse list than one with a gap in it.
func engineProbeCivitaiLogins(ctx context.Context, hits []engineSearchHit, urls []string) {
	if len(hits) == 0 {
		return
	}
	// One budget for the whole page, not one per row: this rides on the admin path while
	// somebody is waiting for a list, and a page where three rows are slow must not cost three
	// timeouts in a row.
	c, cancel := context.WithTimeout(ctx, engineLoginProbeBudget)
	defer cancel()
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < engineLoginProbeWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				hits[i].LoginRequired = engineProbeLogin(c, urls[i])
			}
		}()
	}
	for i := range hits {
		if strings.TrimSpace(urls[i]) == "" {
			continue
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}

// engineLoginProbeBudget bounds the whole page. Twenty round trips eight at a time is three
// waves, so this is generous for a healthy upstream and short enough that a sick one costs the
// search a pause rather than a timeout.
const engineLoginProbeBudget = 8 * time.Second

// engineProbeLogin is one HEAD. "" whenever the status does not answer the question.
func engineProbeLogin(ctx context.Context, target string) string {
	if !strings.HasPrefix(target, "https://") && !strings.HasPrefix(target, "http://") {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if err != nil {
		return ""
	}
	resp, err := engineIngestHTTP.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return engineLoginYes
	case resp.StatusCode >= 200 && resp.StatusCode < 400:
		// 🔴 Including the redirect: measured 2026-09-12, an anonymously downloadable asset
		// answers 307 to the CDN and never 200, so a check for 2xx alone reports every free
		// model as unknown.
		return engineLoginNo
	}
	return ""
}
