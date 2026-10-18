// Package cleaner holds what the app takes from the repository's root: the
// list of changes, shown in the app and used for the release notes. The app
// itself is in src/.
package cleaner

import _ "embed"

// Changelog is CHANGELOG.md (English and Bulgarian for every version).
//
//go:embed CHANGELOG.md
var Changelog string
