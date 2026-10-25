package main

import root "xki4o/cleaner" // the list of changes (CHANGELOG.md at the root) is shipped inside the exe

// appVersion is the version of this build. A release on GitHub is tagged
// "v" + appVersion, and CHANGELOG.md has a "## appVersion" section for it
// (both are checked by the tests and by the release workflow).
const appVersion = "4.11.0"

var changelogMD = root.Changelog
