package ui

import (
	"fmt"
	"strings"
	"time"

	"kindleiot/internal/calendar"
	"kindleiot/internal/config"
)

type uiStrings struct {
	AlertPrefix         string
	MQTTStatusLabel     string
	MQTTMulti           string
	MQTTOk              string
	OfflineLabel        string
	BacklightLabel      string
	OK                  string
	AwakeOff            string
	AwakeOn             string
	ThemeLightShort     string
	ThemeDarkShort      string
	ThemeLightFull      string
	ThemeDarkFull       string
	StandbyShort        string
	StandbyNow          string
	LockBtn             string
	DimNever            string
	DimAfterSec         string
	NightWindowFmt      string
	WebOff              string
	PagePrev            string
	PageOf              string
	PageNext            string
	SwitchOff           string
	SwitchOn            string
	SwitchUnknown       string
	SwitchOnShort       string
	SwitchOffShort      string
	MacroSub            string
	TapAgainConfirm     string
	ConfirmAction       string
	RunBtn              string
	RunSceneBtn         string
	RunShort            string
	ConfirmShort        string
	Waiting             string
	WaitingChart        string
	MinLabel            string
	MaxLabel            string
	EmptyFloorplan      string
	SendErrPrefix       string
	AgoSec              string
	AgoMin              string
	AgoHour             string
	SceneTriggered      string
	TimerFinished       string
	ScheduleTriggered   string
	TimerCancel         string
	TimerTurnsOff       string
	TimerTurnsOn        string
	TimerToggles        string
	TimerDefaultSub     string
	Today               string
	Tomorrow            string
	DayAfterTomorrow    string
	AllDaySuffix        string
	DefaultEventTitle   string
	CalendarTitle       string
	EventsCount         string
	CalendarEnableHint  string
	CalendarEmptyHint   string
	ForecastTitle       string
	WeatherModalPrefix  string
	CloseBtn            string
	WeatherLoading      string
	WindFmt             string
	HourlyTitle         string
	HourlyEmpty         string
	RainMaxFmt          string
	WeeklyTitle         string
	DailyEmpty          string
	TodayFmt            string
	CalendarModalTitle  string
	CalendarModalOff    string
	CalendarModalEmpty  string
	NoData              string
	UpdatedPrefix       string
	TrendStable         string
	TrendRising         string
	TrendFalling        string
	StatMin             string
	StatAvg             string
	StatMax             string
	NoHistory           string
	SampleCountFmt      string
	RangeLabelFmt       string
	PinWrong            string
	PinTitle            string
	PinPrompt           string
	StandbyHeaderSuffix string
	StandbyWeatherSub   string
	NextEventPrefix     string
	StandbyWakePrompt   string
}

var langEN = uiStrings{
	AlertPrefix:         "⚠ ALERT: %s = %s%s (limit %s%s)",
	MQTTStatusLabel:     "MQTT: ",
	MQTTMulti:           "● MQTT %d/%d",
	MQTTOk:              "● MQTT OK",
	OfflineLabel:        "OFFLINE",
	BacklightLabel:      "Backlight: %d / 24",
	OK:                  "OK",
	AwakeOff:            "Keep Awake: OFF",
	AwakeOn:             "Keep Awake: ON",
	ThemeLightShort:     "◐ Light",
	ThemeDarkShort:      "◐ Dark",
	ThemeLightFull:      "◐ Theme: Light",
	ThemeDarkFull:       "◐ Theme: Dark",
	StandbyShort:        "☾ Standby",
	StandbyNow:          "☾ Standby Now",
	LockBtn:             "Lock",
	DimNever:            "Auto-dim: Always on",
	DimAfterSec:         "Auto-dim: %ds",
	NightWindowFmt:      " | Night %d–%dh (lvl %d)",
	WebOff:              "Web: disabled",
	PagePrev:            "◀ Prev",
	PageOf:              "Page %d / %d",
	PageNext:            "Next ▶",
	SwitchOff:           "OFF",
	SwitchOn:            "ON",
	SwitchUnknown:       "UNKNOWN",
	SwitchOnShort:       "● ON",
	SwitchOffShort:      "OFF",
	MacroSub:            "Macro scene (%d actions)",
	TapAgainConfirm:     "Tap again to confirm",
	ConfirmAction:       "⚠ CONFIRM ACTION",
	RunBtn:              "▶ TRIGGER",
	RunSceneBtn:         "▶ RUN SCENE",
	RunShort:            "▶ RUN",
	ConfirmShort:        "CONFIRM",
	Waiting:             "waiting",
	WaitingChart:        "waiting for data",
	MinLabel:            "▼ min %s%s",
	MaxLabel:            "▲ max %s%s",
	EmptyFloorplan:      "Empty floorplan — draw rooms in the web editor",
	SendErrPrefix:       "Publish error: ",
	AgoSec:              "%ds ago",
	AgoMin:              "%dm ago",
	AgoHour:             "%dh ago",
	SceneTriggered:      "✓ Scene triggered: ",
	TimerFinished:       "✓ Timer finished: ",
	ScheduleTriggered:   "● Schedule triggered: ",
	TimerCancel:         "✕ Cancel",
	TimerTurnsOff:       "Turns off",
	TimerTurnsOn:        "Turns on",
	TimerToggles:        "Toggles",
	TimerDefaultSub:     "Countdown timer",
	Today:               "Today",
	Tomorrow:            "Tomorrow",
	DayAfterTomorrow:    "Day +2",
	AllDaySuffix:        " (all day)",
	DefaultEventTitle:   "Event",
	CalendarTitle:       "Calendar",
	EventsCount:         "%d ev.",
	CalendarEnableHint:  "Enable .ics calendar in Settings",
	CalendarEmptyHint:   "No events (14 days)",
	ForecastTitle:       "Weather",
	WeatherModalPrefix:  "Weather • ",
	CloseBtn:            "✕ Close",
	WeatherLoading:      "Waiting for weather data (Open-Meteo)…",
	WindFmt:             "Wind %.0f km/h",
	HourlyTitle:         "Hourly Forecast (24h)",
	HourlyEmpty:         "No hourly data",
	RainMaxFmt:          "Max rain %d%%",
	WeeklyTitle:         "7-Day Forecast",
	DailyEmpty:          "No daily data",
	TodayFmt:            "Today %d/%d",
	CalendarModalTitle:  "Upcoming Events",
	CalendarModalOff:    "Calendar disabled — add .ics feed URLs in Web Settings",
	CalendarModalEmpty:  "No upcoming events in the next 14 days",
	NoData:              "no data",
	UpdatedPrefix:       "Updated ",
	TrendStable:         "• Stable",
	TrendRising:         "▲ Rising",
	TrendFalling:        "▼ Falling",
	StatMin:             "▼ MINIMUM",
	StatAvg:             "∅ AVERAGE",
	StatMax:             "▲ MAXIMUM",
	NoHistory:           "No history samples recorded yet",
	SampleCountFmt:      "History samples: %d",
	RangeLabelFmt:       "Chart range: %s – %s%s",
	PinWrong:            "⚠ WRONG PIN",
	PinTitle:            "Security PIN",
	PinPrompt:           "Enter security PIN code",
	StandbyHeaderSuffix: " • Standby Screensaver",
	StandbyWeatherSub:   "Today %.0f° / %.0f° • Rain %d%%",
	NextEventPrefix:     "NEXT EVENT • ",
	StandbyWakePrompt:   "Tap anywhere to wake the control panel",
}

var langCS = uiStrings{
	AlertPrefix:         "⚠ ALARM: %s = %s%s (limit %s%s)",
	MQTTStatusLabel:     "MQTT: ",
	MQTTMulti:           "● MQTT %d/%d",
	MQTTOk:              "● MQTT OK",
	OfflineLabel:        "OFFLINE",
	BacklightLabel:      "Podsvícení: %d / 24",
	OK:                  "OK",
	AwakeOff:            "Bez uspání: VYP",
	AwakeOn:             "Bez uspání: ZAP",
	ThemeLightShort:     "◐ Světlý",
	ThemeDarkShort:      "◐ Tmavý",
	ThemeLightFull:      "◐ Motiv: Světlý",
	ThemeDarkFull:       "◐ Motiv: Tmavý",
	StandbyShort:        "☾ Spořič",
	StandbyNow:          "☾ Spořič nyní",
	LockBtn:             "Zamknout",
	DimNever:            "Zhasnutí: trvale svítí",
	DimAfterSec:         "Zhasnutí: %ds",
	NightWindowFmt:      " | Noc %d–%dh (jas %d)",
	WebOff:              "Web: vypnuto",
	PagePrev:            "◀ Zpět",
	PageOf:              "Strana %d / %d",
	PageNext:            "Další ▶",
	SwitchOff:           "VYPNUTO",
	SwitchOn:            "ZAPNUTO",
	SwitchUnknown:       "NEZNÁMÝ",
	SwitchOnShort:       "● ZAP",
	SwitchOffShort:      "VYP",
	MacroSub:            "Makro scéna (%d akcí)",
	TapAgainConfirm:     "Klepni znovu pro potvrzení",
	ConfirmAction:       "⚠ POTVRDIT AKCI",
	RunBtn:              "▶ SPUSTIT",
	RunSceneBtn:         "▶ SPUSTIT SCÉNU",
	RunShort:            "▶ AKCE",
	ConfirmShort:        "POTVRDIT",
	Waiting:             "čekám",
	WaitingChart:        "čekám na data grafu",
	MinLabel:            "▼ min %s%s",
	MaxLabel:            "▲ max %s%s",
	EmptyFloorplan:      "Prázdný půdorys — nakresli místnosti ve webovém editoru",
	SendErrPrefix:       "Chyba odeslání: ",
	AgoSec:              "před %ds",
	AgoMin:              "před %dm",
	AgoHour:             "před %dh",
	SceneTriggered:      "✓ Scéna spuštěna: ",
	TimerFinished:       "✓ Časovač dokončen: ",
	ScheduleTriggered:   "● Plán spuštěn: ",
	TimerCancel:         "✕ Zrušit",
	TimerTurnsOff:       "Vypne",
	TimerTurnsOn:        "Zapne",
	TimerToggles:        "Přepne",
	TimerDefaultSub:     "Kuchyňská minutka",
	Today:               "Dnes",
	Tomorrow:            "Zítra",
	DayAfterTomorrow:    "Pozítří",
	AllDaySuffix:        " (celý)",
	DefaultEventTitle:   "Událost",
	CalendarTitle:       "Kalendář",
	EventsCount:         "%d udál.",
	CalendarEnableHint:  "Zapněte .ics kalendář v nastavení",
	CalendarEmptyHint:   "Žádné události (14 dní)",
	ForecastTitle:       "Předpověď",
	WeatherModalPrefix:  "Počasí • ",
	CloseBtn:            "✕ Zavřít",
	WeatherLoading:      "Načítám počasí (Open-Meteo)…",
	WindFmt:             "Vítr %.0f km/h",
	HourlyTitle:         "Hodinový vývoj (24h)",
	HourlyEmpty:         "Bez hodinových dat",
	RainMaxFmt:          "Srážky max %d%%",
	WeeklyTitle:         "Výhled na 7 dní",
	DailyEmpty:          "Bez denních dat",
	TodayFmt:            "Dnes %d.%d.",
	CalendarModalTitle:  "Kalendář událostí",
	CalendarModalOff:    "Kalendář není aktivní — zadejte .ics odkaz ve webovém nastavení",
	CalendarModalEmpty:  "Žádné nadcházející události v dalších 14 dnech",
	NoData:              "žádná data",
	UpdatedPrefix:       "Aktualizováno ",
	TrendStable:         "• Stabilní",
	TrendRising:         "▲ Stoupá",
	TrendFalling:        "▼ Klesá",
	StatMin:             "▼ MINIMUM",
	StatAvg:             "∅ PRŮMĚR",
	StatMax:             "▲ MAXIMUM",
	NoHistory:           "Zatím nejsou v paměti žádné body historie",
	SampleCountFmt:      "Počet vzorků v historii: %d",
	RangeLabelFmt:       "Rozsah grafu: %s – %s%s",
	PinWrong:            "⚠ CHYBNÝ PIN",
	PinTitle:            "Bezpečnostní PIN",
	PinPrompt:           "Zadejte bezpečnostní PIN kód",
	StandbyHeaderSuffix: " • Pohotovostní spořič",
	StandbyWeatherSub:   "Dnes %.0f° / %.0f° • Srážky %d%%",
	NextEventPrefix:     "DALŠÍ UDÁLOST • ",
	StandbyWakePrompt:   "Klepněte kamkoliv pro probuzení ovládacího panelu",
}

func stringsForLang(lang string) uiStrings {
	if config.NormalizeLanguage(lang) == "cs" {
		return langCS
	}
	return langEN
}

func formatAgeLang(d time.Duration, tr uiStrings) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf(tr.AgoSec, int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf(tr.AgoMin, int(d.Minutes()))
	}
	return fmt.Sprintf(tr.AgoHour, int(d.Hours()))
}

func formatWeekdayShort(wd time.Weekday, lang string) string {
	if config.NormalizeLanguage(lang) == "cs" {
		days := [...]string{"Ne", "Po", "Út", "St", "Čt", "Pá", "So"}
		return days[wd]
	}
	days := [...]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	return days[wd]
}

func formatDateLine(t time.Time, lang string) string {
	if config.NormalizeLanguage(lang) == "cs" {
		days := [...]string{"Neděle", "Pondělí", "Úterý", "Středa", "Čtvrtek", "Pátek", "Sobota"}
		line := fmt.Sprintf("%s %d. %d. %d", days[t.Weekday()], t.Day(), int(t.Month()), t.Year())
		if nd := CzechNameDay(t); nd != "" {
			line += " • " + nd
		}
		return line
	}
	days := [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	months := [...]string{"", "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	return fmt.Sprintf("%s, %s %d, %d", days[t.Weekday()], months[t.Month()], t.Day(), t.Year())
}

func formatWeatherHeaderRight(t time.Time, lang string) string {
	if config.NormalizeLanguage(lang) == "cs" {
		if nd := CzechNameDay(t); nd != "" {
			return "Svátek: " + nd
		}
		return ""
	}
	months := [...]string{"", "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	return fmt.Sprintf("%s, %s %d", formatWeekdayShort(t.Weekday(), "en"), months[t.Month()], t.Day())
}

func formatDailyRowDate(parsed time.Time, idx int, lang string) string {
	if config.NormalizeLanguage(lang) == "cs" {
		if idx == 0 {
			return fmt.Sprintf("Dnes %d.%d.", parsed.Day(), int(parsed.Month()))
		}
		return fmt.Sprintf("%s %d.%d.", formatWeekdayShort(parsed.Weekday(), "cs"), parsed.Day(), int(parsed.Month()))
	}
	if idx == 0 {
		return fmt.Sprintf("Today %d/%d", int(parsed.Month()), parsed.Day())
	}
	return fmt.Sprintf("%s %d/%d", formatWeekdayShort(parsed.Weekday(), "en"), int(parsed.Month()), parsed.Day())
}

func formatEventWhenLang(ev calendar.Event, now time.Time, lang string) string {
	tr := stringsForLang(lang)
	st := ev.Start
	nowY, nowM, nowD := now.Date()
	tmrw := now.AddDate(0, 0, 1)
	tmrwY, tmrwM, tmrwD := tmrw.Date()
	y, m, d := st.Date()

	var dayLbl string
	switch {
	case y == nowY && m == nowM && d == nowD:
		dayLbl = tr.Today
	case y == tmrwY && m == tmrwM && d == tmrwD:
		dayLbl = tr.Tomorrow
	default:
		if config.NormalizeLanguage(lang) == "cs" {
			dayLbl = fmt.Sprintf("%s %d.%d.", formatWeekdayShort(st.Weekday(), "cs"), d, int(m))
		} else {
			dayLbl = fmt.Sprintf("%s %d/%d", formatWeekdayShort(st.Weekday(), "en"), int(m), d)
		}
	}
	if ev.AllDay {
		if dayLbl == tr.Today || dayLbl == tr.Tomorrow {
			return dayLbl + tr.AllDaySuffix
		}
		return dayLbl
	}
	return fmt.Sprintf("%s %02d:%02d", dayLbl, st.Hour(), st.Minute())
}

func localizedEventSummary(ev calendar.Event, tr uiStrings) string {
	if strings.TrimSpace(ev.Summary) == "" {
		return tr.DefaultEventTitle
	}
	return ev.Summary
}
