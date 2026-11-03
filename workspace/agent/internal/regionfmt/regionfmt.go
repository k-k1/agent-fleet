// Package regionfmt writes a date and time the way the member's region format asks, for text
// the Agent renders itself (a chat notice, a report note). The Console does the same through
// console/src/lib/intl.ts with Intl; Go has no CLDR data, so the five tags the Settings list
// offers (REGION_FORMATS) are a small table here.
//
// "auto" (or "", or a tag outside the table) is not a layout of its own: the caller's layout
// is used as it is, so an unset member sees exactly what the Agent wrote before the setting
// existed. The zone is the explicit timeZone, else the browser's published zone, else time.Local
// (uiprefs.ZoneName); a name that is not a loadable IANA zone is skipped, and nothing here
// returns an error.
package regionfmt

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// layouts are the minute-precision date-time forms of each tag Settings offers.
var layouts = map[string]string{
	"ja-JP": "2006/01/02 15:04",
	"en-US": "01/02/2006, 3:04 PM",
	"en-GB": "02/01/2006, 15:04",
	"de-DE": "02.01.2006, 15:04",
	"fr-FR": "02/01/2006 15:04",
}

// Location is the member's zone: their explicit timeZone setting, else the browser's zone the
// Console published (uiprefs.ZoneName), else time.Local.
func Location() *time.Location {
	return zone(uiprefs.EffectiveTimeZone())
}

func zone(name string) *time.Location {
	if name == "" || name == "Local" {
		return time.Local
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.Local
	}
	return loc
}

// DateTime formats t to the minute for the member: in their explicit zone, and in their region
// format's layout when one is set. fallbackLayout is what "auto" produces.
func DateTime(t time.Time, fallbackLayout string) string {
	// One read for both values, so a PUT in between cannot hand us region and zone of different
	// generations.
	p := uiprefs.Read()
	region, _ := p["regionFormat"].(string)
	if region == "auto" {
		region = ""
	}
	return format(t, fallbackLayout, region, uiprefs.ZoneName(p))
}

func format(t time.Time, fallbackLayout, region, tz string) string {
	layout := fallbackLayout
	if l, ok := layouts[region]; ok {
		layout = l
	}
	return t.In(zone(tz)).Format(layout)
}
