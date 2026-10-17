package main

// Automatic cleaning: a task in Windows' Task Scheduler starts the app with
// --auto-clean on the chosen schedule. It cleans only the everyday
// categories (caches and temporary files the apps rebuild): nothing that
// needs a restore point, nothing sensitive (Recycle Bin, recent files,
// clipboard, old Windows), and no app that is open at that moment. The
// categories unticked on the Cleanup page are left out too.

import (
	"fmt"
	"strings"
	"time"
)

const autoTaskName = `xKi4o Cleaner\Automatic cleaning`

// The categories automatic cleaning may touch.
var autoCleanIDs = []string{
	"temp", "inetcache", "storeapps", "msstore", "dumps", "thumbs", "dns",
	"chrome", "edge", "brave", "vivaldi", "opera", "firefox",
	"discord", "vscode", "slack", "teams", "notion", "telegram", "steam", "epic", "ubisoft", "battlenet", "eaapp", "gog", "riot", "obs", "devcache",
	"nvinstallers", "nvapp", "amdinstallers", "winlogs",
}

// autoCleanPick: the categories for one automatic run, given what was
// unticked on the Cleanup page (choices[id] == false).
func autoCleanPick(choices map[string]any) []string {
	out := []string{}
	for _, id := range autoCleanIDs {
		if v, ok := choices[id].(bool); ok && !v {
			continue
		}
		out = append(out, id)
	}
	return out
}

var autoFreqs = map[string]bool{"daily": true, "weekly": true, "monthly": true}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}

// autoStart: the first run, at hour:00 local time — tomorrow (daily,
// weekly) or on the 1st of next month (monthly); never right away.
func autoStart(now time.Time, hour int, freq string) time.Time {
	if freq == "monthly" {
		return time.Date(now.Year(), now.Month()+1, 1, hour, 0, 0, 0, now.Location())
	}
	return time.Date(now.Year(), now.Month(), now.Day()+1, hour, 0, 0, 0, now.Location())
}

// autoTaskXML: the task, as Task Scheduler takes it. It runs as the user
// who set it up, with the app's rights, only while they are signed in; a
// run missed while the PC was off happens when it is on again. It does not
// wake the PC and does not start on battery.
func autoTaskXML(exe, user, freq string, start time.Time, desc string) (string, error) {
	if !autoFreqs[freq] {
		return "", fmt.Errorf("unknown schedule %q", freq)
	}
	var sched string
	switch freq {
	case "daily":
		sched = "<ScheduleByDay><DaysInterval>1</DaysInterval></ScheduleByDay>"
	case "weekly":
		sched = "<ScheduleByWeek><DaysOfWeek><" + start.Weekday().String() + " /></DaysOfWeek><WeeksInterval>1</WeeksInterval></ScheduleByWeek>"
	case "monthly":
		sched = "<ScheduleByMonth><DaysOfMonth><Day>" + fmt.Sprint(start.Day()) + "</Day></DaysOfMonth><Months>" +
			"<January /><February /><March /><April /><May /><June /><July /><August /><September /><October /><November /><December />" +
			"</Months></ScheduleByMonth>"
		if start.Day() > 28 {
			return "", fmt.Errorf("day %d is not in every month", start.Day())
		}
	}
	userID := ""
	if user != "" {
		userID = "<UserId>" + xmlEscape(user) + "</UserId>"
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Author>xKi4o Cleaner</Author><Description>` + xmlEscape(desc) + `</Description></RegistrationInfo>
  <Triggers><CalendarTrigger><StartBoundary>` + start.Format("2006-01-02T15:04:05") + `</StartBoundary><Enabled>true</Enabled>` + sched + `</CalendarTrigger></Triggers>
  <Principals><Principal id="Author">` + userID + `<LogonType>InteractiveToken</LogonType><RunLevel>HighestAvailable</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>true</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>true</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings><StopOnIdleEnd>false</StopOnIdleEnd><RestartOnIdle>false</RestartOnIdle></IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT1H</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author"><Exec><Command>` + xmlEscape(exe) + `</Command><Arguments>--auto-clean</Arguments></Exec></Actions>
</Task>
`, nil
}

// taskCommand: the program a task's XML starts.
func taskCommand(x string) string {
	i := strings.Index(x, "<Command>")
	j := strings.Index(x, "</Command>")
	if i < 0 || j < i {
		return ""
	}
	v := x[i+len("<Command>") : j]
	return strings.NewReplacer("&quot;", `"`, "&apos;", "'", "&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(strings.Trim(strings.TrimSpace(v), `"`))
}
