package p2p

import "testing"

// TestRendezvousNamespaceDerivesFromSecret prueba que el mismo secreto da el
// mismo namespace y secretos distintos dan namespaces distintos.
func TestRendezvousNamespaceDerivesFromSecret(t *testing.T) {
	a := rendezvousNamespace("secreto-compartido")
	b := rendezvousNamespace("secreto-compartido")
	c := rendezvousNamespace("otro-secreto")

	if a != b {
		t.Fatalf("el mismo secreto dio namespaces distintos: %q != %q", a, b)
	}
	if a == c {
		t.Fatalf("secretos distintos dieron el mismo namespace: %q", a)
	}
	if len(a) == 0 {
		t.Fatal("namespace vacío")
	}
}
