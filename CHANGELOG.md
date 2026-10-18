# Changelog

Every version has an English and a Bulgarian list. The app shows the list of
a new version before it updates, and once after updating.

## 3.4.0 — 2026-10-18

### English
- Updates: a "Select all" button for each category (Windows, drivers)
- Added Quick fixes on the Repair page: restart Explorer, fix icons, reset the Store, reset Winsock
- Added a connection check on the Network page: ping, jitter and lost packets
- Added "Copy list" on the Programs page
- 7 new speed tweaks in the Tweaker
- The Repair page scrolls in a small window

### Български
- Обновления: бутон „Избери всички“ за всяка категория (Windows, драйвери)
- Добавени „Бързи поправки“ в раздел „Поправка“: рестарт на Explorer, поправка на иконите, нулиране на Store и на Winsock
- Добавена проверка на връзката в раздел „Мрежа“: ping, колебание и изгубени пакети
- Добавено „Копирай списъка“ в раздел „Програми“
- 7 нови tweak-а за производителност в Tweaker
- Раздел „Поправка“ вече се скролва в малък прозорец

## 3.3.0 — 2026-10-18

### English
- Added the Updates page: search and install Windows and driver updates
- Longer, smoother loading animation
- Animations across the app: pages, cards, buttons and the menu
- Keeping a new refresh rate is now asked in a notification on top
- Removed the Right-click menu page; entries it hid can be shown again in the Restore Center
- "Do it all" no longer suggests updating programs
- Fixed: NVIDIA's driver list said "no drivers for this card"

### Български
- Добавен раздел „Обновления“: търсене и инсталиране на обновления за Windows и драйвери
- По-дълга и по-плавна анимация при зареждане
- Анимации в цялата програма: страници, карти, бутони и менюто
- Запазването на нова честота на опресняване се пита с известие най-отгоре
- Премахнат раздел „Контекстно меню“; скритите от него записи се връщат от Центъра за възстановяване
- „Направи всичко“ вече не предлага обновяване на програми
- Поправено: списъкът с драйвери на NVIDIA казваше „няма драйвери за тази карта“

## 3.2.0 — 2026-10-18

### English
- New logo — in the app, on its icon and while it loads
- New loading animation in the Graphite colours
- Added the Graphics card page: live sensors, checks and details
- Added going back to an older graphics driver: one on the PC, or a recent one from NVIDIA
- Added removing the graphics driver like DDU, with a copy to put it back
- Added a check and a one-click fix for screens below their refresh rate
- Cloudflare is now the recommended DNS; Quad9 was removed
- Fixed Windows' white scrollbars in the Disk Analyzer and other lists
- The credits page in the new look

### Български
- Ново лого — в програмата, на иконата и при зареждане
- Нова анимация при зареждане в цветовете на Графит
- Добавен раздел „Видеокарта“: сензори на живо, проверки и подробности
- Добавено връщане към по-стар драйвер на видеокартата — от компютъра или от NVIDIA
- Добавено премахване на драйвера на видеокартата като DDU, с копие за връщане
- Добавена проверка и поправка с един клик за екрани под максималната им честота
- Cloudflare е препоръчаният DNS; Quad9 е премахнат
- Поправени белите плъзгачи на Windows в „Анализ на диска“ и други списъци
- Страницата „За програмата“ в новия вид

## 3.1.0 — 2026-10-17

### English
- New look: the Graphite theme, the Inter font and an emerald accent
- PC health is now a dashboard with live processor, graphics card, memory, drive and network
- Added the graphics card's temperature, load and video memory (NVIDIA; load for other cards)
- Added quick settings, help and presets cards on PC health
- Added a theme choice in Settings: Graphite or the old Ocean
- The 3.0 tweaks stay marked New until a version brings new ones

### Български
- Нов вид: тема Графит, шрифт Inter и изумруден акцент
- „Здраве на компютъра“ вече е табло с процесор, видеокарта, памет, диск и мрежа на живо
- Добавени температура, натоварване и видео памет на видеокартата (NVIDIA; натоварване за другите)
- Добавени бързи настройки, помощ и пресети в „Здраве на компютъра“
- Добавен избор на тема в Настройки: Графит или старият Океан
- Туийковете от 3.0 остават отбелязани като нови, докато някоя версия не добави нови

## 3.0.1 — 2026-10-17

### English
- Added presets in the Tweaker: Essential, Gaming, Privacy and Ultimate
- Fixed the motherboard shown twice in System info on self-built PCs

### Български
- Добавени пресети в Tweaker: Основен, Игри, Поверителност и Максимален
- Поправено: дънната платка се показваше два пъти в „Информация“ при сглобени компютри

## 3.0.0 — 2026-10-17

### English
- Added System info: processor, graphics, memory, motherboard, drives, monitors, security and battery, with tips
- Added Network: switch the DNS server after a speed test, and flush the DNS cache
- Added the right-click menu: hide entries that programs added, and show them again
- Added automatic cleaning on a schedule (Settings)
- Added 18 tweaks; new tweaks are shown first and marked New
- Added a Windows notification when a long job ends while the app is in the tray
- Added a line about the PC on PC health

### Български
- Добавена информация за системата: процесор, видеокарта, памет, дънна платка, дискове, монитори, защита и батерия, със съвети
- Добавена Мрежа: смяна на DNS сървъра след тест на скоростта и изчистване на DNS кеша
- Добавено контекстно меню: скриване на записите, които програмите са добавили, и връщането им
- Добавено автоматично почистване по график (Настройки)
- Добавени 18 туийка; новите излизат най-отгоре с надпис „Ново“
- Добавено известие от Windows, когато дълга задача приключи, докато програмата е в трея
- Добавен ред за компютъра в „Здраве на PC“

## 2.10.0 — 2026-10-16

### English
- Added optimization for HDDs (defragmentation with progress and Stop), not only TRIM for SSDs
- Added an analysis of how fragmented an HDD is
- Added the details of each disk: S.M.A.R.T. counters by name, Windows' log of disk errors and what can help
- Added a read-only check and a bad sector check (chkdsk /r) of a drive from its details
- Removed Epic Games and other launchers that update themselves from program updates
- Added a clear message when an update removes the program instead of updating it

### Български
- Добавена оптимизация и за HDD дисковете (дефрагментация с прогрес и „Спри“), не само TRIM за SSD
- Добавен анализ колко е фрагментиран един HDD
- Добавени подробности за всеки диск: S.M.A.R.T. показателите с имена, грешките на диска в дневника на Windows и какво може да помогне
- Добавени проверка само за четене и проверка за лоши сектори (chkdsk /r) на диск от подробностите му
- Махнати Epic Games и другите лаунчери, които се обновяват сами, от обновяването на програми
- Добавено ясно съобщение, когато обновяване премахне програмата, вместо да я обнови

## 2.9.1 — 2026-10-16

### English
- Fixed shortcuts that Windows still opens (the program was moved) being listed as broken: Windows itself is asked now

### Български
- Поправено: преки пътища, които Windows още отваря (програмата е преместена), се показваха като счупени — вече се пита самият Windows

## 2.9.0 — 2026-10-16

### English
- Added a new logo
- Added the app's own title bar with a button that hides the app in the tray, next to the clock
- Added a setting to use Windows' title bar instead
- Fixed the app opening behind other windows (e.g. the browser)
- Fixed starting the app again not bringing the open one to the front (also from the tray)
- Fixed tweak categories counting tweaks this Windows does not have
- Changed rows that scroll sideways to fade out where there is more
- Changed the README to a short one

### Български
- Добавено ново лого
- Добавена собствена заглавна лента с бутон, който скрива програмата в трея до часовника
- Добавена настройка за заглавната лента на Windows
- Поправено: програмата се отваряше зад други прозорци (напр. браузъра)
- Поправено: второ пускане не изкарваше отворената програма отпред (и от трея)
- Поправено: категориите в Туийкъра броиха и туийкове, които този Windows няма
- Редовете, които се превъртат настрани, избледняват там, където има още
- README е кратък

## 2.8.1 — 2026-10-16

### English
- Fixed working shortcuts being listed as broken: every path a shortcut stores is checked, and the 32/64-bit Program Files twin
- Fixed game mode listing a program's helpers on their own (e.g. Steam Client WebHelper): they are counted with their program
- Added reading the NVMe health log for power-on hours and wear when Windows does not give them

### Български
- Поправено: работещи преки пътища се показваха като счупени — проверяват се всички пътища в прекия път и двойникът в 32/64-битовия Program Files
- Поправено: режим „Игри“ показваше помощните процеси на програма отделно (напр. Steam Client WebHelper) — вече се броят с програмата
- Добавено четене на дневника за здраве на NVMe дисковете за часовете работа и износването, когато Windows не ги дава

## 2.8.0 — 2026-10-16

### English
- Removed Large files (the Disk Analyzer lists the largest files)
- Added a scan of drive C: when the app starts, so the Disk Analyzer is ready (can be turned off in Settings)
- Fixed Microsoft Edge, Chrome and Brave not updating through winget: their own updater is used
- Fixed updates failing for programs that are open (e.g. OBS): they are closed first and started again after
- Added the reason, "Try again" and "Website" for updates winget cannot do (403, installed another way)
- Added 13 tweaks: no startup delay, fewer animations, game priority, no network throttling, no Start recommendations, no Settings suggestions, no transparency, no recent files, no "- Shortcut", compact view, faster folders, no automatic restart after updates, Edge not in the background
- Fixed game mode offering xKi4o Cleaner itself and setups running from Temp
- Fixed starting programs again after game mode when the app runs without administrator rights

### Български
- Премахнати „Големи файлове“ (Анализът на диска показва най-големите файлове)
- Добавено сканиране на диск C: при стартиране, за да е готов Анализът на диска (изключва се от Настройки)
- Поправено: Microsoft Edge, Chrome и Brave не се обновяваха през winget — ползва се техният собствен обновител
- Поправено: обновяването на отворени програми (напр. OBS) се проваляше — затварят се преди това и се пускат отново след него
- Добавени причина, „Опитай пак“ и „Сайт“ за обновления, които winget не може да направи (403, инсталирана по друг начин)
- Добавени 13 туийка: без забавяне при стартиране, по-малко анимации, приоритет за игрите, без ограничаване на мрежата, без препоръки в „Старт“, без предложения в Настройки, без прозрачност, без скорошни файлове, без „ - Пряк път“, компактен изглед, по-бързи папки, без автоматичен рестарт след ъпдейти, Edge не във фона
- Поправено: режим „Игри“ предлагаше самия xKi4o Cleaner и инсталатори, пуснати от Temp
- Поправено: пускането на програмите след режим „Игри“, когато програмата работи без администраторски права

## 2.7.0 — 2026-10-16

### English
- **PC health** is the new start page: a score with what lowers it (junk, memory, free space, disk health, restore points, errors in the log, program updates) and **"Do all recommended"** — the chosen steps go through the job queue. Program updates are never pre-ticked.
- **Programs — an uninstaller like Revo.** Every installed program with its size, install date and when it was last used. Uninstalling runs the program's own uninstaller and waits for it (and for whatever it starts), then looks for leftovers in three depths (safe, moderate, advanced): its folders in Program Files, ProgramData and AppData, shortcuts, registry keys. You review the list: certain ones are ticked, your settings and data are never pre-ticked. Files go to the Recycle Bin; registry keys are saved to a .reg file first and can be put back from the Restore Center. Windows keys, services and startup entries are never touched. Store apps can be removed too, and programs whose uninstaller is gone can be removed by force.
- **Program updates through winget:** the programs that have a newer version, updated one by one or all together, each with its own result.
- **Disk health (S.M.A.R.T.)** in Optimize: temperature, SSD wear, power-on time and errors, with a plain verdict. A disk in trouble is warned about a few seconds after the app starts.
- **Game mode:** one click closes the programs you chose and frees RAM; turning it off starts them again (without administrator rights). It never changes the power plan, services or Windows settings.
- **Empty folders and broken shortcuts:** finds empty folders (a folder holding only empty folders counts too) in your folders, and shortcuts on the desktop, in the Start menu and Quick Launch that point to nothing. Removed ones go to the Recycle Bin.

### Български
- **Здраве на компютъра** е новият начален екран: оценка и какво я сваля (боклуци, памет, свободно място, здраве на дисковете, точки за възстановяване, грешки в дневника, обновления на програми) и бутон **„Направи препоръчаното“** — избраните стъпки минават през опашката със задачи. Обновленията на програми никога не са отметнати предварително.
- **Програми — деинсталатор като Revo.** Всички инсталирани програми с размер, дата на инсталиране и кога са ползвани за последно. Деинсталирането пуска собствения деинсталатор на програмата и го изчаква (заедно с всичко, което той пусне), после търси остатъци на три нива (безопасно, умерено, разширено): папките ѝ в Program Files, ProgramData и AppData, преки пътища, ключове в регистъра. Ти преглеждаш списъка: сигурните са отметнати, твоите настройки и данни никога не са отметнати предварително. Файловете отиват в кошчето, а ключовете първо се записват в .reg файл и се връщат от Центъра за възстановяване. Ключове на Windows, услуги и стартиращи записи никога не се пипат. Премахват се и Store приложения, а програми без деинсталатор — принудително.
- **Обновяване на програмите през winget:** кои програми имат нова версия, обновяване една по една или всички наведнъж, всяка със свой резултат.
- **Здраве на дисковете (S.M.A.R.T.)** в „Оптимизация“: температура, износване на SSD, работни часове и грешки, с ясна оценка. За диск с проблем програмата предупреждава няколко секунди след стартиране.
- **Режим „Игри“:** с едно кликване затваря избраните от теб програми и освобождава RAM; при изключване ги пуска отново (без администраторски права). Никога не променя плана за захранване, услугите или настройките на Windows.
- **Празни папки и счупени преки пътища:** намира празни папки (и папки, в които има само празни папки) в твоите папки, и преки пътища на работния плот, в менюто „Старт“ и Quick Launch, които сочат към нищо. Премахнатите отиват в кошчето.

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
