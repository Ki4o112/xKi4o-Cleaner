package main

import (
	"fmt"
	"sync/atomic"
)

// Texts produced by the backend (notes, errors, dialogs) in both languages.
// The page translates everything else itself.

var lang atomic.Value // "bg" or "en"

func init() { lang.Store("en") } // English until the saved setting says otherwise

func setLang(l string) {
	if l == "en" || l == "bg" {
		lang.Store(l)
	}
}

var texts = map[string][2]string{ // [bg, en]
	"blocked.admin":    {"Нужни са администраторски права", "Administrator rights are needed"},
	"blocked.pending":  {"Изчаква рестарт след инсталиране на ъпдейти", "Waiting for the restart after updates"},
	"skip.rp":          {"Пропуснато — точката за възстановяване не се създаде", "Skipped — the restore point could not be created"},
	"skip.pending":     {"Windows инсталира ъпдейти — опитай след рестарт", "Windows is installing updates — try after a restart"},
	"skip.open":        {"Програмата е отворена", "The app is open"},
	"err.recycle":      {"Кошчето не се изпразни", "The Recycle Bin was not emptied"},
	"err.drivers":      {"Windows не позволи премахването", "Windows did not allow the removal"},
	"err.dism":         {"DISM върна код %d", "DISM returned code %d"},
	"err.unexpected":   {"Неочаквана грешка: %v", "Unexpected error: %v"},
	"err.clipboard":    {"Клипбордът е зает от друга програма", "Another app is using the clipboard"},
	"trim.unsupported": {"Не се поддържа", "Not supported"},
	"rp.admin":         {"Нужни са администраторски права.", "Administrator rights are needed."},
	"repair.dism":      {"DISM — проверка и поправка на хранилището на Windows", "DISM — checking and repairing the Windows component store"},
	"repair.sfc":       {"SFC — проверка на системните файлове", "SFC — checking system files"},
	"repair.chkdsk":    {"CHKDSK — проверка на диска %s (само четене)", "CHKDSK — checking drive %s (read-only)"},
	"repair.unknown":   {"Непознат инструмент.", "Unknown tool."},
	"repair.nostart":   {"%s не можа да стартира.", "%s could not start."},
	"repair.chkbad":    {"CHKDSK откри проблеми — виж изхода по-долу.", "CHKDSK found problems — see the output below."},
	"repair.chkok":     {"Не са открити грешки на диска.", "No disk errors were found."},
	"repair.dismbad":   {"DISM приключи с код %d.", "DISM finished with code %d."},
	"repair.dismok":    {"DISM приключи успешно.", "DISM finished successfully."},
	"repair.sfcok":     {"SFC приключи. Ако е поправил файлове, рестартирай компютъра.", "SFC finished. If it repaired files, restart the computer."},
	"tw.err.unknown":   {"Непознат туийк.", "Unknown tweak."},
	"tw.err.rp":        {"Точката за възстановяване не се създаде, затова нищо не е променено. Включи защитата на системата или изключи изискването в „Настройки“.", "The restore point could not be created, so nothing was changed. Turn System Protection on, or turn the requirement off in Settings."},
	"tw.err.partial":   {"Част от промените не минаха: %s", "Some changes did not go through: %s"},
	"tw.err.oneway":    {"Тази промяна не може да се върне от програмата.", "This change cannot be undone from the app."},
	"tw.err.code":      {"Командата върна код %d.", "The command returned code %d."},
	"tw.err.winget":    {"Не е намерен winget (App Installer). Инсталирай го от Microsoft Store и опитай пак.", "winget (App Installer) was not found. Install it from the Microsoft Store and try again."},
	"tw.note.store":    {"Отворих страницата за инсталиране.", "Opened the install page."},
	"tw.err.tamper":    {"Windows Security не позволи промяната. Изключи „Tamper Protection“ в Windows Security и опитай пак.", "Windows Security blocked the change. Turn off Tamper Protection in Windows Security and try again."},
	"tw.err.ultimate":  {"Този компютър не поддържа плана Ultimate Performance (често при лаптопи с Modern Standby).", "This PC does not support the Ultimate Performance plan (common on laptops with Modern Standby)."},
	"tw.err.wt":        {"Не намерих настройката за профил по подразбиране в Windows Terminal.", "The default profile setting was not found in Windows Terminal."},
	"tw.err.ps7":       {"PowerShell 7 не можа да се инсталира. Провери интернета и опитай пак.", "PowerShell 7 could not be installed. Check the internet connection and try again."},
	"tw.err.net":       {"Windows не прие мрежовите настройки.", "Windows did not accept the network settings."},
	"tw.err.noapps":    {"Не са избрани приложения.", "No apps were selected."},
	"tw.err.edge":      {"Microsoft Edge не се премахна. В някои версии на Windows това не е разрешено.", "Microsoft Edge was not removed. Some Windows versions do not allow it."},
	"tw.err.onedrive":  {"OneDrive не се деинсталира.", "OneDrive was not uninstalled."},
	"tw.err.nvidia":    {"Драйверът на NVIDIA върна грешка %d.", "The NVIDIA driver returned error %d."},
	"tw.na.win11":      {"Само за Windows 11", "Windows 11 only"},
	"tw.na.build":      {"Нужна е по-нова версия на Windows", "Needs a newer version of Windows"},
	"tw.na.defender":   {"Windows Defender не е активният антивирус", "Windows Defender is not the active antivirus"},
	"tw.na.tamper":     {"Tamper Protection е включена — изключи я първо в Windows Security", "Tamper Protection is on — turn it off in Windows Security first"},
	"tw.na.nvidia":     {"Няма видеокарта или драйвер на NVIDIA", "No NVIDIA graphics card or driver"},
	"tw.na.wt":         {"Windows Terminal не е инсталиран", "Windows Terminal is not installed"},
	"tw.na.recall":     {"Recall го има само в Windows 11 24H2 и по-нови", "Recall exists only on Windows 11 24H2 and newer"},
	"tw.na.bitlocker":  {"Дискът не е криптиран с BitLocker", "The drive is not encrypted with BitLocker"},
	"box.webview": {"За интерфейса на програмата е нужен Microsoft Edge WebView2 Runtime, а той липсва на този компютър.\n\nДа отворя ли страницата за изтегляне? След като го инсталираш, пусни програмата отново.",
		"The app's interface needs the Microsoft Edge WebView2 Runtime, which is missing on this computer.\n\nOpen the download page? Start the app again after installing it."},
	"box.closeBusy": {"Работата още не е приключила (почистване или туийк). Ако затвориш сега, ще спре по средата.\n\nДа затворя ли програмата?",
		"A job (cleaning or a tweak) has not finished yet. Closing now stops it halfway.\n\nClose the app?"},
	"box.closeCritical": {"В момента работи системен инструмент на Windows (DISM, SFC, Windows Update или драйвери). Ако затвориш програмата, той ще довърши сам във фонов режим, но няма да видиш резултата.\n\nДа затворя ли програмата?",
		"A Windows system tool is running right now (DISM, SFC, Windows Update or drivers). If you close the app it finishes on its own in the background, but you won't see the result.\n\nClose the app?"},
}

// T returns the text for key in the current language.
func T(key string, args ...any) string {
	v, ok := texts[key]
	if !ok {
		return key
	}
	s := v[0]
	if lang.Load() == "en" {
		s = v[1]
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}
