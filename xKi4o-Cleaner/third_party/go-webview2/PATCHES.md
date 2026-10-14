# Changes in this copy of go-webview2

This folder is a copy of [jchv/go-webview2](https://github.com/jchv/go-webview2)
at commit `56598839c808` (MIT License, see `LICENSE`), with these changes for
xKi4o Cleaner:

- **Bindings run off the UI thread.** `msgcb` answers each JavaScript call in
  its own goroutine, so a slow Go function never freezes the window.
- **App-style WebView settings.** When `Debug` is false, browser shortcuts
  (reload, print, find), zoom, pinch zoom, swipe navigation and the status bar
  are turned off.
- **`WebViewOptions.Background`** — 0xRRGGBB colour for the window class brush
  and the WebView2 default background, so there is no white flash on start.
- **`WebViewOptions.OnClose`** — called on `WM_CLOSE`; returning `false` keeps
  the window open (used to confirm closing while a job runs).
- **`WindowOptions.OnCreated`** — called after the window is created and
  before it is shown (used to give the title bar dark colours).
- The window class gets the standard arrow cursor; `internal/w32` gained
  `CreateSolidBrush` and `LoadCursorW`.
- The `cmd/` demo folder was removed.
