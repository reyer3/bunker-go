package mail

import (
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

// TestMailDoesNotEditDeleteOrReact pins that mail stays out of edit,
// delete and react (issues #76, #17): sent mail cannot be changed, so
// core.Service answers ErrUnsupported for it instead of pretending.
func TestMailDoesNotEditDeleteOrReact(t *testing.T) {
	var a any = &Adapter{}
	if _, ok := a.(core.Editor); ok {
		t.Error("mail implements core.Editor")
	}
	if _, ok := a.(core.Deleter); ok {
		t.Error("mail implements core.Deleter")
	}
	if _, ok := a.(core.Reactor); ok {
		t.Error("mail implements core.Reactor")
	}
}
