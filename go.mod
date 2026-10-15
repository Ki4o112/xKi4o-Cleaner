module xki4o/cleaner

go 1.24

require (
	github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808
	golang.org/x/sys v0.35.0
)

require github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect

// A lightly patched copy of go-webview2 lives in third_party/go-webview2
// (see third_party/go-webview2/PATCHES.md).
replace github.com/jchv/go-webview2 => ./third_party/go-webview2
