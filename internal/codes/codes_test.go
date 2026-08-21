package codes

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorfCarriesTheCodeAndTheMessage(t *testing.T) {
	err := Errorf(NotReady, "task %d is %s", 7, "doing")
	if got, want := err.Code, NotReady; got != want {
		t.Errorf("code = %q, want %q", got, want)
	}
	if got, want := err.Error(), "NOT_READY: task 7 is doing"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestOfReadsTheCodeThroughAWrap(t *testing.T) {
	wrapped := fmt.Errorf("dispatch: %w", Errorf(AtCapacity, "2 workers are live"))
	if got, want := Of(wrapped), AtCapacity; got != want {
		t.Errorf("Of(wrapped) = %q, want %q", got, want)
	}
}

func TestOfCallsAnUnnamedFailureUnavailable(t *testing.T) {
	if got, want := Of(errors.New("connection refused")), Unavailable; got != want {
		t.Errorf("Of(plain) = %q, want %q", got, want)
	}
	if got, want := Of(nil), Code(""); got != want {
		t.Errorf("Of(nil) = %q, want empty", got)
	}
}
