package main

// Start with Windows: the app starts at sign-in, hidden in the notification
// area, so game mode and the automatic memory cleaning go on watching.
// Windows never starts a program that asks for administrator rights from the
// Run key or the Startup folder, so a shortcut in the Startup folder runs a
// scheduled task (schtasks /run) that starts the app with the highest rights
// and no question. The shortcut is what Task Manager's Startup apps list
// shows: turning it off there turns the start off. Free of Windows calls.

const bootTaskName = `xKi4o Cleaner\Start with Windows`

// bootLnkArgs: what the Startup shortcut gives schtasks.exe.
func bootLnkArgs() string { return `/run /tn "` + bootTaskName + `"` }

// bootTaskXML: the task for this user and this copy of the app (no trigger:
// the Startup shortcut runs it).
func bootTaskXML(exe, user, desc string) string {
	userID := ""
	if user != "" {
		userID = "<UserId>" + xmlEscape(user) + "</UserId>"
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Author>xKi4o Cleaner</Author><Description>` + xmlEscape(desc) + `</Description></RegistrationInfo>
  <Triggers />
  <Principals><Principal id="Author">` + userID + `<LogonType>InteractiveToken</LogonType><RunLevel>HighestAvailable</RunLevel></Principal></Principals>
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
    <Priority>4</Priority>
  </Settings>
  <Actions Context="Author"><Exec><Command>` + xmlEscape(exe) + `</Command><Arguments>--tray</Arguments></Exec></Actions>
</Task>
`
}
