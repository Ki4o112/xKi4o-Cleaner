package main

import (
	"strings"
	"testing"
)

func TestDelayTask(t *testing.T) {
	if n := delayTaskName(`Discord <x>:/\|?*`); n != delayFolder+"Discord x" {
		t.Fatalf("name %q", n)
	}
	if n := delayTaskName("Steam Клиент"); n != delayFolder+"Steam Клиент" {
		t.Fatalf("name %q", n)
	}
	if delayTaskName("***") != delayFolder+"app" {
		t.Fatal("empty name")
	}
	x := delayTaskXML(`C:\Apps\Discord\Update.exe`, `--processStart "Discord.exe"`, `C:\Apps\Discord`, "S-1-5-21-1", 60, "desc & more")
	for _, want := range []string{"<Delay>PT60S</Delay>", "<RunLevel>LeastPrivilege</RunLevel>", "<UserId>S-1-5-21-1</UserId>", "<Arguments>--processStart &quot;Discord.exe&quot;</Arguments>", "<WorkingDirectory>C:\\Apps\\Discord</WorkingDirectory>", "desc &amp; more", "<Priority>5</Priority>"} {
		if !strings.Contains(x, want) {
			t.Errorf("no %s in\n%s", want, x)
		}
	}
	if strings.Contains(delayTaskXML("a.exe", " ", "", "", 30, ""), "<Arguments>") {
		t.Error("empty arguments")
	}
	if !delaySecsOK(60) || delaySecsOK(5) {
		t.Fatal("secs")
	}
	l := setDelay(nil, startDelay{Key: "a", Secs: 60})
	l = setDelay(l, startDelay{Key: "b", Secs: 30})
	l = setDelay(l, startDelay{Key: "a", Secs: 120})
	if len(l) != 2 || l[1].Secs != 120 {
		t.Fatalf("%+v", l)
	}
	if l = setDelay(l, startDelay{Key: "a"}); len(l) != 1 || l[0].Key != "b" {
		t.Fatalf("%+v", l)
	}
}
