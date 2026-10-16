package main

import _ "embed" // the list of changes is shipped inside the exe

// appVersion is the version of this build. A release on GitHub is tagged
// "v" + appVersion, and CHANGELOG.md has a "## appVersion" section for it
// (both are checked by the tests and by the release workflow).
const appVersion = "2.9.1"

//go:embed CHANGELOG.md
var changelogMD string
