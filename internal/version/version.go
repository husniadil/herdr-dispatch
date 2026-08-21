// Package version is what this binary calls itself and what contract it
// claims to satisfy. Both are facts about the code rather than settings, and
// they live in one place because more than one surface repeats them: the
// binary's own `version`, doctor's report, and the plugin manifest Herdr
// installs, which a test holds to the same string.
package version

// Version is the dispatcher's own version. The manifest's version matches it.
const Version = "0.1.0"

// Contract is the version of the Herdr plugin contract this binary satisfies.
// It is this plugin's own conformance, and not the board's: doctor relays
// htask's contract beside it, and the two move independently.
const Contract = "0.5.0"
