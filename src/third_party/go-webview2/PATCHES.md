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
- **Bindings are also kept in `window.__go`.** A page function or variable
  with the same name as a binding replaces `window[name]`; the page calls
  `window.__go[name]`, which nothing else writes.
- The window class gets the standard arrow cursor; `internal/w32` gained
  `CreateSolidBrush` and `LoadCursorW`.
- **`WebViewOptions.OnMessage`** — sees every window message first; when it
  returns true its answer is used (the app's own title bar and tray icon).
- **`WebView.SetVisible`** — tells WebView2 when the window is hidden in the
  tray and shown again (else a window hidden from the start stays empty).
- **`WebView.SetTopInset`** — leaves a few pixels above the page (the
  window's top edge for resizing with the app's own title bar).
- The big and the small window icon are loaded each at its own size.
- After moving or resizing the window, the page gets the keyboard back.
- The `cmd/` demo folder was removed.
- `WindowOptions.Hidden`: the window is made without being shown (the app
  started in the tray shows no empty window while WebView2 starts).
