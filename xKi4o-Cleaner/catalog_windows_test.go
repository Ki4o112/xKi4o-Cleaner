//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// TestCatalogCleansOnlyCaches builds fake app folders, runs the real
// cleaning rules on them and checks that caches go and user data stays.
func TestCatalogCleansOnlyCaches(t *testing.T) {
	root := t.TempDir()
	envs := map[string]string{
		"LOCALAPPDATA": filepath.Join(root, "Local"),
		"APPDATA":      filepath.Join(root, "Roaming"),
		"TEMP":         filepath.Join(root, "Local", "Temp"),
		"SystemRoot":   filepath.Join(root, "Windows"),
		"ProgramData":  filepath.Join(root, "ProgramData"),
		"USERPROFILE":  root,
	}
	for k, v := range envs {
		old := os.Getenv(k)
		os.Setenv(k, v)
		defer os.Setenv(k, old)
	}
	steam := filepath.Join(root, "Steam")
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Valve\Steam`, registry.SET_VALUE|registry.QUERY_VALUE)
	if err == nil {
		oldSteam, _, _ := k.GetStringValue("SteamPath")
		_ = k.SetStringValue("SteamPath", strings.ReplaceAll(steam, `\`, "/"))
		defer func() {
			if oldSteam == "" {
				_ = k.DeleteValue("SteamPath")
			} else {
				_ = k.SetStringValue("SteamPath", oldSteam)
			}
			k.Close()
		}()
	}
	amd := filepath.Join(systemDrive()+`\`, "AMD")
	amdExisted := dirExists(amd)

	L, R, PD := envs["LOCALAPPDATA"], envs["APPDATA"], envs["ProgramData"]
	teams := filepath.Join(L, `Packages\MSTeams_8wekyb3d8bbwe\LocalCache\Microsoft\MSTeams\EBWebView`)
	gone := []string{
		filepath.Join(L, `Google\Chrome\User Data\Default\Cache\Cache_Data\f_000001`),
		filepath.Join(L, `Google\Chrome\User Data\Default\Code Cache\js\a`),
		filepath.Join(L, `Google\Chrome\User Data\Profile 1\GPUCache\data_0`),
		filepath.Join(R, `discord\Cache\Cache_Data\x`),
		filepath.Join(R, `discord\logs\x.log`),
		filepath.Join(teams, `Default\Cache\Cache_Data\x`),
		filepath.Join(R, `Microsoft\Teams\Cache\x`),
		filepath.Join(L, `Battle.net\Cache\x`),
		filepath.Join(L, `Battle.net\Logs\x.log`),
		filepath.Join(L, `Battle.net\BrowserCaches\common\Cache\x`),
		filepath.Join(L, `Electronic Arts\EA Desktop\WebCache\x`),
		filepath.Join(L, `Electronic Arts\EA Desktop\Logs\x.log`),
		filepath.Join(PD, `GOG.com\Galaxy\logs\x.log`),
		filepath.Join(PD, `GOG.com\Galaxy\webcache\common\Cache\x`),
		filepath.Join(L, `Riot Games\Riot Client\Logs\x.log`),
		filepath.Join(R, `obs-studio\logs\x.txt`),
		filepath.Join(L, `NVIDIA Corporation\NVIDIA App\CefCache\Default\Cache\x`),
		filepath.Join(L, `NVIDIA Corporation\NVIDIA App\app.log`),
		filepath.Join(L, `Packages\Microsoft.WindowsStore_8wekyb3d8bbwe\LocalCache\x`),
		filepath.Join(root, `AppData\LocalLow\Intel\ShaderCache\x`),
		filepath.Join(steam, `logs\x.txt`),
		filepath.Join(L, `EpicGamesLauncher\Saved\Logs\x.log`),
		filepath.Join(amd, `xKi4oTest_Installer\setup.exe`),
	}
	keep := []string{
		filepath.Join(L, `Google\Chrome\User Data\Default\History`),
		filepath.Join(L, `Google\Chrome\User Data\Default\Login Data`),
		filepath.Join(L, `Google\Chrome\User Data\Default\Local Storage\leveldb\x.log`),
		filepath.Join(L, `Google\Chrome\User Data\Default\Service Worker\CacheStorage\a`),
		filepath.Join(R, `discord\Local Storage\leveldb\x`),
		filepath.Join(teams, `Default\Cookies`),
		filepath.Join(L, `Battle.net\Account\x`),
		filepath.Join(L, `Battle.net\BrowserCaches\common\Cookies`),
		filepath.Join(R, `obs-studio\basic\scenes\s.json`),
		filepath.Join(PD, `GOG.com\Galaxy\storage\galaxy-2.0.db`),
		filepath.Join(L, `NVIDIA Corporation\NVIDIA App\settings.json`),
		filepath.Join(L, `Electronic Arts\EA Desktop\user.ini`),
		filepath.Join(L, `Packages\Microsoft.WindowsStore_8wekyb3d8bbwe\Settings\settings.dat`),
		filepath.Join(steam, `steamapps\common\Game\game.exe`),
		filepath.Join(L, `Riot Games\Riot Client\Data\RiotGamesPrivateSettings.yaml`),
	}
	for _, f := range append(append([]string{}, gone...), keep...) {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		if !amdExisted {
			_ = os.RemoveAll(amd)
		} else {
			_ = os.RemoveAll(filepath.Join(amd, "xKi4oTest_Installer"))
		}
	}()

	initPaths()
	buildCatalog()
	ids := []string{"chrome", "discord", "teams", "battlenet", "eaapp", "gog", "riot", "obs", "nvapp", "msstore", "shaders", "steam", "epic"}
	if !amdExisted {
		ids = append(ids, "amdinstallers")
	}
	for _, id := range ids {
		it := findItem(id)
		if it == nil {
			t.Fatalf("no item %s", id)
		}
		if !it.available() {
			t.Errorf("%s not detected", id)
			continue
		}
		ts := it.targets()
		for _, tg := range ts {
			if !safeTarget(tg.Path) {
				t.Errorf("%s: unsafe target %s", id, tg.Path)
			}
			in := strings.HasPrefix(strings.ToLower(tg.Path), strings.ToLower(root)) ||
				strings.HasPrefix(strings.ToLower(tg.Path), strings.ToLower(amd))
			if !in {
				t.Errorf("%s: target outside the test folders: %s", id, tg.Path)
			}
		}
		cleanTargets(ts, nil)
	}
	for _, f := range gone {
		if strings.HasPrefix(f, amd) && amdExisted {
			continue
		}
		if exists(f) {
			t.Errorf("not cleaned: %s", strings.TrimPrefix(f, root))
		}
	}
	for _, f := range keep {
		if !exists(f) {
			t.Errorf("user data removed: %s", strings.TrimPrefix(f, root))
		}
	}
}

// TestAnalyzeAllSmoke scans the real (test machine's) folders once: every
// item must report back and nothing may panic.
func TestAnalyzeAllSmoke(t *testing.T) {
	initPaths()
	buildCatalog()
	got := map[string]bool{}
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	analyzeAll(false, func(r report) {
		<-mu
		got[r.ID] = true
		mu <- struct{}{}
	})
	for _, it := range catalog {
		if !got[it.id] {
			t.Errorf("no report for %s", it.id)
		}
	}
}
