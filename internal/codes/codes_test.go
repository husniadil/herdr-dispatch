package codes

import (
	"errors"
	"fmt"
	"testing"
)

// §6.3: a plugin MAY define sub-reasons inside message, and never a top-level
// code outside the list. Every sub-reason this binary has answers under one of
// the nine, and keeps its own name as the first word of the sentence so a
// caller reading the message loses nothing the old code told it.
func TestEverySubReasonAnswersUnderAContractCode(t *testing.T) {
	contract := map[Code]bool{
		Usage: true, NotFound: true, Unavailable: true, Timeout: true,
		Conflict: true, Unsupported: true, Forbidden: true, Denied: true,
		Unexpected: true,
	}
	for reason, code := range carries {
		if !contract[code] {
			t.Errorf("%s answers under %q, which is not one of the §6.3 nine", reason, code)
		}
		err := Refusef(reason, "something")
		if err.Code != code {
			t.Errorf("Refusef(%s).Code = %q, want %q", reason, err.Code, code)
		}
		if got, want := err.Error(), string(code)+": "+string(reason)+": something"; got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
		if got := ReasonOf(err); got != reason {
			t.Errorf("ReasonOf = %q, want %q", got, reason)
		}
	}
}

// The mapping itself, spelled out: an audit reads this rather than the map.
func TestTheSubReasonsMapOntoTheCodesTheContractFixes(t *testing.T) {
	for reason, want := range map[Reason]Code{
		Invalid:           Usage,
		NoBasePane:        Unsupported,
		NotReady:          Conflict,
		AtCapacity:        Conflict,
		AlreadyDispatched: Conflict,
		AlreadyRunning:    Conflict,
		NotRunning:        Conflict,
	} {
		if got := Refusef(reason, "x").Code; got != want {
			t.Errorf("%s answers %q, want %q", reason, got, want)
		}
	}
}

// §6.3 fixes the exit status of each code, and a caller scripting three
// sibling plugins reads the same number from each.
func TestExitIsTheStatusTheContractFixes(t *testing.T) {
	for code, want := range map[Code]int{
		Usage: 2, NotFound: 3, Unavailable: 4, Timeout: 5, Conflict: 6,
		Unsupported: 7, Forbidden: 8, Denied: 9, Unexpected: 1,
	} {
		if got := Exit(code); got != want {
			t.Errorf("Exit(%s) = %d, want %d", code, got, want)
		}
	}
	if got := Exit(Code("SOMETHING_ELSE")); got != 1 {
		t.Errorf("Exit(unknown) = %d, want UNEXPECTED's 1", got)
	}
}

func TestErrorfCarriesTheCodeAndTheMessage(t *testing.T) {
	err := Errorf(NotFound, "task %d is %s", 7, "gone")
	if got, want := err.Code, NotFound; got != want {
		t.Errorf("code = %q, want %q", got, want)
	}
	if got, want := err.Error(), "NOT_FOUND: task 7 is gone"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestOfReadsTheCodeThroughAWrap(t *testing.T) {
	wrapped := fmt.Errorf("dispatch: %w", Refusef(AtCapacity, "2 workers are live"))
	if got, want := Of(wrapped), Conflict; got != want {
		t.Errorf("Of(wrapped) = %q, want %q", got, want)
	}
	if got, want := ReasonOf(wrapped), AtCapacity; got != want {
		t.Errorf("ReasonOf(wrapped) = %q, want %q", got, want)
	}
}

func TestOfCallsAnUnnamedFailureUnavailable(t *testing.T) {
	if got, want := Of(errors.New("connection refused")), Unavailable; got != want {
		t.Errorf("Of(plain) = %q, want %q", got, want)
	}
	if got, want := Of(nil), Code(""); got != want {
		t.Errorf("Of(nil) = %q, want empty", got)
	}
	if got := ReasonOf(errors.New("connection refused")); got != "" {
		t.Errorf("ReasonOf(plain) = %q, want empty", got)
	}
}
