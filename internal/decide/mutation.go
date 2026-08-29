package decide

import (
	"path/filepath"
	"strings"
)

// ReasonNoCodeToMutate is why a self-review shot was not sent: the submission
// it would have examined has no code in its diff and names no test, so the
// mutation pass — a compiling mutation per claimed guard, the tests the report
// names, the failure confirmed — has nothing to bite on.
//
// It is a REASON on the trail rather than a silence, because the shot not
// firing is a decision this daemon made about a submission and the operator
// reading the trail is the one judging that submission.
const ReasonNoCodeToMutate = "no code to mutate"

// NothingToMutate reports whether the evidence in hand leaves the mutation
// pass no work: no code among the paths the submission's diff touched, and no
// test named in its report.
//
// Both halves have to be empty. Code in the diff is somewhere a mutation can
// land whatever the report says, and a report that names a test is something
// to run whatever the diff holds — a task whose deliverable is a document can
// still name the gate it ran.
//
// A caller that could not read the diff must not call this with an empty
// list: no evidence is not evidence of nothing, and the conservative answer
// there is to send the shot.
func NothingToMutate(changed []string, report string) bool {
	return !holdsCode(changed) && !namesATest(report)
}

// holdsCode reports whether any changed path is a file in a programming
// language, read off its extension. A deliverable with no extension — NOTES,
// a text file, a Markdown document — is not something a compiling mutation
// can be written against.
func holdsCode(changed []string) bool {
	for _, path := range changed {
		if codeExtensions[strings.ToLower(filepath.Ext(path))] {
			return true
		}
	}
	return false
}

// codeExtensions is the languages a worker on this fleet writes in, plus the
// ones a change is plausibly filed in. A language missing from it costs one
// skipped shot and never a wrong claim, which is the direction to be wrong in:
// the operator still reviews the submission and the trail says why the shot
// did not fire.
var codeExtensions = map[string]bool{
	".go": true, ".rs": true, ".py": true, ".rb": true, ".java": true,
	".kt": true, ".kts": true, ".scala": true, ".swift": true, ".m": true,
	".mm": true, ".c": true, ".h": true, ".cc": true, ".cpp": true,
	".hpp": true, ".cs": true, ".zig": true, ".js": true, ".jsx": true,
	".ts": true, ".tsx": true, ".mjs": true, ".cjs": true, ".php": true,
	".pl": true, ".lua": true, ".ex": true, ".exs": true, ".erl": true,
	".hs": true, ".clj": true, ".cljs": true, ".dart": true, ".jl": true,
	".nim": true, ".ml": true, ".fs": true, ".r": true, ".sh": true,
	".bash": true, ".zsh": true, ".fish": true, ".sql": true, ".vue": true,
	".svelte": true, ".groovy": true, ".gradle": true,
}

// namesATest reports whether the report names a test to run. The word is what
// the mutation pass asks the worker to run — "the tests your report names" —
// so a report that carries one gives the shot something to do even where the
// diff carries no code.
//
// The match is on the WORD: "latest" is not a test, and neither is a path that
// happens to end in it. A Go test name — TestTheGoalIsDelivered — starts one,
// which is why the boundary is only checked on the left.
func namesATest(report string) bool {
	lower := strings.ToLower(report)
	for i := 0; ; {
		at := strings.Index(lower[i:], "test")
		if at < 0 {
			return false
		}
		at += i
		if at == 0 || !isWordRune(rune(lower[at-1])) {
			return true
		}
		i = at + len("test")
	}
}

func isWordRune(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}
