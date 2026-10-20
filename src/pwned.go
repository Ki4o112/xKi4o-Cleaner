package main

// "Has my password leaked?" — Have I Been Pwned's Pwned Passwords range API
// (k-anonymity): only the first 5 characters of the password's SHA-1 leave
// the PC, never the password or its whole hash. The answer is every leaked
// hash that starts with them; the match is found here. Padding hides from an
// onlooker how many came back. Free of Windows calls.

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var pwnedAPI = "https://api.pwnedpasswords.com/range/"

type pwnedResult struct {
	Count int64  `json:"count"` // how many times it is in known leaks (0: not found)
	Error string `json:"error,omitempty"`
}

// pwnedCount finds suffix (upper-case hex) in a range answer ("SUFFIX:COUNT"
// lines). Padding lines have the count 0.
func pwnedCount(body io.Reader, suffix string) int64 {
	sc := bufio.NewScanner(body)
	for sc.Scan() {
		h, c, ok := strings.Cut(strings.TrimSpace(sc.Text()), ":")
		if ok && strings.EqualFold(h, suffix) {
			n, _ := strconv.ParseInt(strings.TrimSpace(c), 10, 64)
			return n
		}
	}
	return 0
}

func pwnedCheck(pw string) pwnedResult {
	if pw == "" {
		return pwnedResult{}
	}
	sum := sha1.Sum([]byte(pw))
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pwnedAPI+h[:5], nil)
	if err != nil {
		return pwnedResult{Error: err.Error()}
	}
	req.Header.Set("User-Agent", "xKi4o-Cleaner/"+appVersion)
	req.Header.Set("Add-Padding", "true")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return pwnedResult{Error: T("pw.err.net")}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return pwnedResult{Error: fmt.Sprintf("%s: %s", T("pw.err.net"), resp.Status)}
	}
	return pwnedResult{Count: pwnedCount(io.LimitReader(resp.Body, 4<<20), h[5:])}
}
