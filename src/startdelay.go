package main

// Delayed start: a program that starts with Windows can start a minute or
// two later instead, so the desktop is ready sooner. Its own entry is turned
// off (as on the Startup page) and a sign-in task of the user's, without
// administrator rights, starts the same command after the delay. Taking the
// delay back removes the task and turns the entry on again. Free of Windows
// calls.

import (
	"strconv"
	"strings"
)

const delayFolder = `xKi4o Cleaner\Delayed\`

// startDelay: one delayed program.
type startDelay struct {
	Key  string `json:"key"` // the startup entry
	Name string `json:"name"`
	Task string `json:"task"` // the task's name in Task Scheduler
	Secs int    `json:"secs"`
}

// delayTaskName: a task name from the entry's name (only letters, digits,
// spaces and a few signs; Task Scheduler refuses some).
func delayTaskName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == ' ' || r == '-' || r == '_' || r == '.' || r == '(' || r == ')':
			b.WriteRune(r)
		case r < 128 && (r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			b.WriteRune(r)
		case r >= 128:
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	if s == "" {
		s = "app"
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return delayFolder + s
}

// delayTaskXML: the task that starts exe (with args) secs after sign-in, as
// the user, without administrator rights, at normal priority.
func delayTaskXML(exe, args, dir, user string, secs int, desc string) string {
	userID := ""
	if user != "" {
		userID = "<UserId>" + xmlEscape(user) + "</UserId>"
	}
	argx := ""
	if strings.TrimSpace(args) != "" {
		argx = "<Arguments>" + xmlEscape(args) + "</Arguments>"
	}
	wd := ""
	if dir != "" {
		wd = "<WorkingDirectory>" + xmlEscape(dir) + "</WorkingDirectory>"
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Author>xKi4o Cleaner</Author><Description>` + xmlEscape(desc) + `</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled>` + userID + `<Delay>PT` + strconv.Itoa(secs) + `S</Delay></LogonTrigger></Triggers>
  <Principals><Principal id="Author">` + userID + `<LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>false</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings><StopOnIdleEnd>false</StopOnIdleEnd><RestartOnIdle>false</RestartOnIdle></IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>5</Priority>
  </Settings>
  <Actions Context="Author"><Exec><Command>` + xmlEscape(exe) + `</Command>` + argx + wd + `</Exec></Actions>
</Task>
`
}

// delaySecsOK: the delays the page offers.
func delaySecsOK(s int) bool { return s == 30 || s == 60 || s == 120 || s == 300 }

// setDelay puts, replaces or (secs 0) removes the entry's delay.
func setDelay(list []startDelay, d startDelay) []startDelay {
	out := make([]startDelay, 0, len(list)+1)
	for _, x := range list {
		if x.Key != d.Key {
			out = append(out, x)
		}
	}
	if d.Secs > 0 {
		out = append(out, d)
	}
	return out
}
