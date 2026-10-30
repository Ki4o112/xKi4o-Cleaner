"""Builds w11d.json for the Debloat page from a Win11Debloat checkout
(https://github.com/Raphire/Win11Debloat, MIT licence).

    python3 src/debloat/import.py <path to Win11Debloat>

The .reg files are read here and written as plain operations (set a value,
delete a value, delete a key), which the app carries out itself."""
import json, os, re, subprocess, sys

src = sys.argv[1]
here = os.path.dirname(os.path.abspath(__file__))

def load(p):
    with open(os.path.join(src, p), encoding="utf-8-sig") as f:
        return json.load(f)

def find_reg(name):
    # Windows does not mind the case of a file name; Win11Debloat relies on it
    for d in (os.path.join(src, "Regfiles"), os.path.join(src, "Regfiles", "Undo")):
        for f in os.listdir(d):
            if f.lower() == name.lower():
                return os.path.join(d, f)
    raise SystemExit("no .reg file " + name)

def read_reg(name):
    p = find_reg(name)
    raw = open(p, "rb").read()
    text = raw.decode("utf-16", errors="ignore") if raw[:2] in (b"\xff\xfe", b"\xfe\xff") else raw.decode("utf-8-sig")
    return parse_reg(text.replace("\r", ""), name)

ROOTS = {"HKEY_CURRENT_USER": "HKCU", "HKEY_LOCAL_MACHINE": "HKLM", "HKEY_CLASSES_ROOT": "HKCR", "HKEY_USERS": "HKU"}

def unquote(s):
    out, i = [], 0
    while i < len(s):
        if s[i] == "\\" and i + 1 < len(s):
            out.append(s[i + 1]); i += 2
        else:
            out.append(s[i]); i += 1
    return "".join(out)

def parse_reg(text, name):
    lines, buf = [], ""
    for ln in text.split("\n"):
        s = ln.strip()
        if buf:
            buf += s
        else:
            buf = s
        if buf.endswith("\\") and not buf.startswith("["):
            buf = buf[:-1]
            continue
        lines.append(buf); buf = ""
    ops, key = [], None
    for s in lines:
        if not s or s.startswith(";") or s.startswith("Windows Registry Editor"):
            continue
        if s.startswith("["):
            k = s[1:-1]
            delete = k.startswith("-")
            k = k.lstrip("-")
            root, _, path = k.partition("\\")
            if root not in ROOTS:
                raise SystemExit(f"{name}: root {root}")
            key = ROOTS[root] + "\\" + path
            if delete:
                ops.append({"op": "delkey", "key": key}); key = None
            else:
                ops.append({"op": "mkkey", "key": key})  # a section alone makes the key
            continue
        if key is None:
            raise SystemExit(f"{name}: value outside a key: {s}")
        m = re.match(r'^(@|"((?:[^"\\]|\\.)*)")=(.*)$', s)
        if not m:
            raise SystemExit(f"{name}: cannot read {s}")
        vname = "" if m.group(1) == "@" else unquote(m.group(2))
        v = m.group(3)
        if v == "-":
            ops.append({"op": "delval", "key": key, "name": vname}); continue
        if v.startswith('"'):
            ops.append({"op": "set", "key": key, "name": vname, "type": "string", "str": unquote(v[1:-1])}); continue
        if v.startswith("dword:"):
            ops.append({"op": "set", "key": key, "name": vname, "type": "dword", "num": int(v[6:], 16)}); continue
        hm = re.match(r"^hex(\((\w+)\))?:(.*)$", v)
        if hm:
            kind = {None: "binary", "2": "expand", "7": "multi", "b": "qword", "0": "binary", "3": "binary"}[hm.group(2)]
            data = [int(x, 16) for x in hm.group(3).split(",") if x.strip()]
            ops.append({"op": "set", "key": key, "name": vname, "type": kind, "bin": data}); continue
        raise SystemExit(f"{name}: value type {v}")
    return ops

feat = load("Config/Features.json")
en = load("Config/Languages/en-US/Features.json")
apps = load("Config/Apps.json")
apps_en = load("Config/Languages/en-US/Apps.json")
defaults = [s["Name"] for s in load("Config/DefaultSettings.json")["Settings"] if s["Value"] is True]

cat_by_name = {c["Name"]: c["CategoryId"] for c in feat["Categories"]}
SKIP = {"RemoveApps", "Apps", "RemoveGamingApps", "RemoveHPApps", "CreateRestorePoint", "ReplaceStart",
        "ReplaceStartAllUsers", "ClearStartAllUsers", "ForceRemoveEdge", "DisableStoreSearchSuggestions"}
CUSTOM = {"DisableWidgets": "widgets", "EnableWindowsSandbox": "sandbox", "EnableWindowsSubsystemForLinux": "wsl", "ClearStart": "clearstart"}
EXTRA_APPS = {"DisableBing": ["Microsoft.BingSearch"], "DisableCopilot": ["Microsoft.Copilot", "XP9CXNGPPJ97XX"]}

out = {"categories": [{"id": c["CategoryId"], "label": c["Name"]} for c in feat["Categories"]], "features": [], "groups": []}
for f in feat["Features"]:
    fid = f["FeatureId"]
    if fid in SKIP:
        continue
    t = en["Features"].get(fid, {})
    x = {"id": fid, "cat": cat_by_name.get(f.get("Category")), "label": t.get("Label", f["Label"]), "tip": t.get("ToolTip", f.get("ToolTip", "")) or "",
         "prio": f.get("Priority") or 99}
    if f.get("MinVersion"): x["min"] = f["MinVersion"]
    if f.get("MaxVersion"): x["max"] = f["MaxVersion"]
    if f.get("RequiresReboot"): x["reboot"] = True
    if f.get("DisableWhenApplied"): x["oneway"] = True
    if f.get("RegistryKey"):
        x["apply"] = read_reg(f["RegistryKey"])
    if f.get("RegistryUndoKey"):
        x["undo"] = read_reg(f["RegistryUndoKey"])
    if fid in CUSTOM:
        x["custom"] = CUSTOM[fid]
    if fid in EXTRA_APPS:
        x["apps"] = EXTRA_APPS[fid]
    if fid == "DisableTelemetry":
        x["tasks"] = True
    if not x.get("apply") and not x.get("custom"):
        raise SystemExit(f"{fid}: nothing to do")
    out["features"].append(x)
ids = {x["id"] for x in out["features"]}
for g in feat["UiGroups"]:
    tg = en["UiGroups"].get(g["GroupId"], {})
    vals = [{"label": tg.get("Values", {}).get(v["FeatureIds"][0], v["Label"]), "ids": [i for i in v["FeatureIds"] if i in ids]} for v in g["Values"]]
    vals = [v for v in vals if v["ids"]]
    if len(vals) < 2:
        continue
    out["groups"].append({"id": g["GroupId"], "cat": g["Category"], "label": tg.get("Label", g["Label"]), "tip": tg.get("ToolTip", g.get("ToolTip", "")), "prio": g.get("Priority") or 99, "values": vals})
out["apps"] = []
for a in apps["Apps"]:
    aid = a["AppId"]
    key = aid if isinstance(aid, str) else aid[0]
    t = apps_en.get(key, {})
    out["apps"].append({"ids": [aid] if isinstance(aid, str) else aid, "name": t.get("FriendlyName", key), "desc": t.get("Description", ""),
                        "rec": a["Recommendation"], "method": a["RemovalMethod"], "def": bool(a["SelectedByDefault"])})
out["presets"] = [{"name": p["Name"], "ids": p["AppIds"]} for p in apps["Presets"]]
out["defaults"] = [d for d in defaults if d in ids]
# the Bulgarian texts (written for the app; Win11Debloat has none)
bg = json.load(open(os.path.join(here, "bg_features.json"), encoding="utf-8"))
bga = json.load(open(os.path.join(here, "bg_apps.json"), encoding="utf-8"))
for c in out["categories"]:
    c["bg"] = bg["categories"][c["id"]]
for x in out["features"]:
    x["bg"] = bg["features"][x["id"]]
for g in out["groups"]:
    t = bg["groups"][g["id"]]
    g["bg"] = t[:2]
    for v, l in zip(g["values"], t[2]):
        v["bg"] = l
for a in out["apps"]:
    a["bg"] = bga[a["ids"][0]]
try:
    out["source"] = "Win11Debloat " + subprocess.check_output(["git", "-C", src, "rev-parse", "--short", "HEAD"], text=True).strip()
except Exception:
    out["source"] = "Win11Debloat"
with open(os.path.join(here, "w11d.json"), "w", encoding="utf-8") as f:
    json.dump(out, f, ensure_ascii=False, indent=1)
print(len(out["features"]), "features,", len(out["groups"]), "groups,", len(out["apps"]), "apps,", sum(len(x.get("apply", [])) + len(x.get("undo", [])) for x in out["features"]), "registry operations")
