package core

import (
	"strings"
	"testing"
)

// Reported by the owner: a send with Cc recipients described both Cc and
// Bcc as "copia oculta" (copy-paste from the Bcc when it landed). The mail
// contract is Cc visible in the header, Bcc hidden in the envelope only,
// so the two must read differently in the dry-run plan.
func TestAssessSendRiskCcIsVisibleCopyNotHidden(t *testing.T) {
	out := Outgoing{
		To:  []string{"a@b.cl"},
		Cc:  []string{"e@f.cl"},
		Bcc: []string{"h@i.cl"},
	}
	_, _, effects := assessSendRisk(out)
	if !strings.Contains(effects, "copia visible a 1 destinatario(s)") {
		t.Errorf("effects = %q, want Cc described as a visible copy", effects)
	}
	if got := strings.Count(effects, "copia oculta"); got != 1 {
		t.Errorf("effects = %q, want exactly one \"copia oculta\" (Bcc only)", effects)
	}
}
