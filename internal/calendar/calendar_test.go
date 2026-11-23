package calendar

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kindleiot/internal/config"
)

func TestParseICS(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, loc)

	ics := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"BEGIN:VEVENT",
		"SUMMARY:Dnesni schuzka\\, dulezita",
		"DESCRIPTION:folded",
		" line2",
		"LOCATION:Zasedacka\\; 2. patro",
		"DTSTART:20261123T143000",
		"DTEND:20261123T153000",
		"END:VEVENT",
		"BEGIN:VEVENT",
		"SUMMARY:Svoz",
		" trideneho odpadu",
		"DTSTART;VALUE=DATE:20261124",
		"DTEND;VALUE=DATE:20261125",
		"END:VEVENT",
		"BEGIN:VEVENT",
		"SUMMARY:UTC event",
		"DTSTART:20261125T080000Z",
		"DTEND:20261125T090000Z",
		"END:VEVENT",
		"BEGIN:VEVENT",
		"SUMMARY:Narozeniny",
		"DTSTART;VALUE=DATE:20201126",
		"RRULE:FREQ=YEARLY",
		"END:VEVENT",
		"BEGIN:VEVENT",
		"SUMMARY:Trenink",
		"DTSTART:20261102T180000",
		"DTEND:20261102T190000",
		"RRULE:FREQ=WEEKLY;UNTIL=20261124T235959",
		"END:VEVENT",
		"BEGIN:VEVENT",
		"SUMMARY:Stara udalost",
		"DTSTART:20261120T100000",
		"DTEND:20261120T110000",
		"END:VEVENT",
		"END:VCALENDAR",
	}, "\r\n")

	evs := ParseICS(strings.NewReader(ics), now, loc)
	if len(evs) != 5 {
		t.Fatalf("expected 5 upcoming events, got %d: %+v", len(evs), evs)
	}

	// Verify chronological order and unescaping / unfolding
	if evs[0].Summary != "Dnesni schuzka, dulezita" || evs[0].Location != "Zasedacka; 2. patro" {
		t.Fatalf("unexpected first event: %+v", evs[0])
	}
	if evs[1].Summary != "Trenink" || evs[1].Start.Day() != 23 || evs[1].Start.Hour() != 18 {
		t.Fatalf("expected weekly recurrence projected to Nov 23 18:00, got %+v", evs[1])
	}
	if !evs[2].AllDay || evs[2].Summary != "Svoztrideneho odpadu" {
		t.Fatalf("expected unfolded all-day event on Nov 24, got %+v", evs[2])
	}
	if evs[3].Summary != "UTC event" || evs[3].Start.Hour() != 9 {
		t.Fatalf("expected UTC 08:00 converted to +01:00 = 09:00, got %+v", evs[3])
	}
	if evs[4].Summary != "Narozeniny" || !evs[4].AllDay || evs[4].Start.Year() != 2026 || evs[4].Start.Day() != 26 {
		t.Fatalf("expected yearly birthday projected to 2026-11-26, got %+v", evs[4])
	}
}

func TestNormalizeFeedURLAndServiceFetchNow(t *testing.T) {
	if got := normalizeFeedURL("  webcal://calendar.example.com/a.ics "); got != "https://calendar.example.com/a.ics" {
		t.Fatalf("expected webcal:// converted to https://, got %q", got)
	}

	today := time.Now().Format("20060102")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/calendar")
		_, _ = w.Write([]byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nSUMMARY:Test HTTP Feed\r\nDTSTART;VALUE=DATE:" + today + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"))
	}))
	defer ts.Close()

	svc := NewService(config.CalendarConfig{
		Enabled: true,
		URLs:    []string{ts.URL},
	}, nil)
	svc.SetHTTPClient(ts.Client())
	svc.FetchNow()

	evs := svc.Events()
	if len(evs) != 1 || evs[0].Summary != "Test HTTP Feed" {
		t.Fatalf("expected 1 fetched event, got %+v", evs)
	}
}
