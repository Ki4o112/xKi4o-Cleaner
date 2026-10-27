package main

// Start with Windows: the app starts at sign-in, hidden in the notification
// area, so game mode and the automatic memory cleaning go on watching.
// Windows never starts a program that asks for administrator rights from the
// Run key or the Startup folder; a sign-in task with the highest rights does,
// as other tools that need them do (MSI Afterburner, Process Lasso). Only
// when the user turns it on. No shortcut that runs the task (schtasks /run):
// Defender takes that chain for malware getting round UAC. Free of Windows calls.

const bootTaskName = `xKi4o Cleaner\Start with Windows`

// bootTaskXML: the task for this user and this copy of the app.
func bootTaskXML(exe, user, desc string) string {
	userID := ""
	if user != "" {
		userID = "<UserId>" + xmlEscape(user) + "</UserId>"
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Author>xKi4o Cleaner</Author><Description>` + xmlEscape(desc) + `</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled>` + userID + `<Delay>PT30S</Delay></LogonTrigger></Triggers>
  <Principals><Principal id="Author">` + userID + `<LogonType>InteractiveToken</LogonType><RunLevel>HighestAvailable</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
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
