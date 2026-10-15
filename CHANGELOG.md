# Changelog

Every version has an English and a Bulgarian list. The app shows the list of
a new version before it updates, and once after updating.

## 2.6.1 — 2026-10-15

### English
- **Updates from GitHub.** When the app starts it checks for a newer release. If there is one, it shows what is new and updates itself with one click — the new version is downloaded, checked (size, SHA-256, a real Windows program) and put in place, then the app restarts. The check can be turned off in Settings.
- **What's new.** After an update the app shows once what changed; the list is also in Settings.
- **Windows logs:** the update logs in `ProgramData\USOShared\Logs\System` belong to Windows alone and cannot be deleted even by an administrator. They are no longer counted and then refused on every cleaning.
- **Error reports:** "Latest problems elsewhere" shows only the last 3 days, each problem once (with how many times it happened).

### Български
- **Ъпдейти от GitHub.** При стартиране програмата проверява за нова версия. Ако има, показва какво е новото и се обновява с едно кликване — новата версия се изтегля, проверява (размер, SHA-256, истинска програма за Windows) и се слага на мястото на старата, после програмата се рестартира. Проверката може да се изключи от Настройки.
- **Какво е новото.** След ъпдейт програмата веднъж показва какво се е променило; списъкът е и в Настройки.
- **Логове на Windows:** логовете на ъпдейтите в `ProgramData\USOShared\Logs\System` са само на Windows и не могат да се изтрият дори от администратор. Вече не се броят и не се отказват при всяко почистване.
- **Доклади за грешки:** „Последни проблеми другаде“ показва само последните 3 дни, всеки проблем по веднъж (с броя пъти).

## 2.6.0 — 2026-10-15

### English
- **Fixed: cleaning did not start** after "Skip locked files" or "Close all" (two page functions had the same names as the app's bindings). The same bug kept **settings from being saved** — they are saved now.
- **Background jobs:** long jobs run one at a time while you use the other sections. A new one waits in a queue (a notice says after which job it starts), and a bar at the bottom shows what runs, its progress and what waits — open, stop or remove from there.
- **Disk Analyzer like TreeSize:** drives with % free, an expandable tree with size, % of the parent folder, files, folders and date, keyboard control, "Expand the largest", a side panel with details and a space map, the largest files and the space by kind and by extension.
- **Real icons** of programs and files in cleanup, processes, startup, the apps in the way, the Disk Analyzer, Large files, Duplicates and the Restore Center.
- **Apps in the way are closed by force** before cleaning (always works; the dialog says unsaved work is lost). Can be turned off in Settings.
- **Parts of Windows are never offered for closing** (anything from the Windows folder or made as part of Windows, e.g. CrossDeviceResume, ShellHost).
- **Restore Center:** a red button deletes all restore points (after a confirmation, optionally keeping the newest).

### Български
- **Поправено: почистването не тръгваше** след „Пропусни заключените файлове“ или „Затвори всички“ (две функции в интерфейса имаха същите имена като връзките към програмата). Същата грешка пречеше **настройките да се записват** — вече се записват.
- **Фонови задачи:** дългите задачи вървят една по една, докато използваш другите раздели. Новата чака на опашка (известие казва след коя започва), а лента отдолу показва какво работи, напредъка и какво чака — оттам се отваря, спира или маха.
- **Анализ на диска като TreeSize:** дисковете с процент свободно място, дърво с размер, % от горната папка, файлове, папки и дата, управление с клавиатурата, „Разгъни най-голямото“, страничен панел с подробности и карта на мястото, най-големите файлове и мястото по вид и по разширение.
- **Истински иконки** на програмите и файловете при почистване, процеси, стартиране, програмите, които пречат, анализа на диска, големите файлове, дубликатите и Центъра за възстановяване.
- **Програмите, които пречат, се затварят принудително** преди почистване (винаги работи; прозорецът казва, че незапазената работа се губи). Може да се изключи от Настройки.
- **Части от Windows никога не се предлагат за затваряне** (всичко от папката на Windows или направено като част от Windows — напр. CrossDeviceResume, ShellHost).
- **Център за възстановяване:** червен бутон изтрива всички точки за възстановяване (след потвърждение, по желание без най-новата).

## 2.5.0 — 2026-10-14

### English
- New sections: **Duplicates** (SHA-256 of the content, the last copy can never be deleted), **Large files**, **Processes**, **Restore Center** and **Logs & error reports** with "Copy report".
- A new sidebar, the command palette (Ctrl+K) and a live CPU/RAM readout.

### Български
- Нови раздели: **Дубликати** (SHA-256 на съдържанието, последното копие никога не може да се изтрие), **Големи файлове**, **Процеси**, **Център за възстановяване** и **Логове и доклади за грешки** с „Копирай доклад“.
- Ново странично меню, команден панел (Ctrl+K) и жив показател за CPU/RAM.
