package main

// Locked folders (the Passwords page): a folder is hidden and every access to
// it is denied until the password is given in this app. The password is kept
// only as a salted PBKDF2-SHA256 hash. This is not encryption: an administrator
// can undo it by hand (icacls … /remove:d *S-1-1-0). Free of Windows calls.

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"path/filepath"
	"strings"
)

type lockedFolder struct {
	Path string `json:"path"`
	Salt string `json:"salt"`
	Hash string `json:"hash"`
	At   int64  `json:"at"`
}

const lockIter = 200_000

func lockHash(pw, saltHex string) string {
	salt, _ := hex.DecodeString(saltHex)
	k, _ := pbkdf2.Key(sha256.New, pw, salt, lockIter, 32)
	return hex.EncodeToString(k)
}

func newLock(path, pw string, at int64) lockedFolder {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	l := lockedFolder{Path: filepath.Clean(path), Salt: hex.EncodeToString(salt), At: at}
	l.Hash = lockHash(pw, l.Salt)
	return l
}

func (l lockedFolder) check(pw string) bool {
	return subtle.ConstantTimeCompare([]byte(lockHash(pw, l.Salt)), []byte(l.Hash)) == 1
}

// lockTargetOK: what may be locked — a folder below a drive's root that is
// not Windows' or a program's, not a whole user profile, and that does not
// hold this app or its data (keep is those paths).
func lockTargetOK(path string, protected bool, profile string, keep ...string) bool {
	norm := func(x string) string { return strings.ToLower(strings.TrimRight(strings.ReplaceAll(x, "/", `\`), `\`)) }
	p := norm(path)
	if protected || len(p) <= 3 || p[1] != ':' || p[2] != '\\' || p == norm(profile) {
		return false
	}
	if parts := strings.Split(p, `\`); len(parts) > 1 {
		switch parts[1] {
		case "windows", "program files", "program files (x86)", "programdata":
			return false // locking a program's folder would stop it
		}
	}
	for _, k := range keep {
		k = norm(k)
		if k != "" && (k == p || strings.HasPrefix(k, p+`\`)) {
			return false
		}
	}
	return true
}
