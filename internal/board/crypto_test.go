package board

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	bc, err := NewBoardCrypto()
	if err != nil {
		t.Fatalf("NewBoardCrypto: %v", err)
	}
	plaintext := []byte("Tareas compartidas: comprar pan, llamar a María")
	ciphertext, err := bc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	decrypted, err := bc.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(plaintext, decrypted) {
		t.Fatalf("Decrypt(Encrypt(plaintext)) = %q, quiere %q", decrypted, plaintext)
	}
}

func TestEncryptEmptyPlaintext(t *testing.T) {
	bc, err := NewBoardCrypto()
	if err != nil {
		t.Fatalf("NewBoardCrypto: %v", err)
	}
	ciphertext, err := bc.Encrypt(nil)
	if err != nil {
		t.Fatalf("Encrypt(nil): %v", err)
	}
	decrypted, err := bc.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if len(decrypted) != 0 {
		t.Fatalf("Decrypt(Encrypt(nil)) = %q, quiere vacía", decrypted)
	}
}

func TestEncryptProducesDifferentCiphertexts(t *testing.T) {
	bc, err := NewBoardCrypto()
	if err != nil {
		t.Fatalf("NewBoardCrypto: %v", err)
	}
	plaintext := []byte("mismo mensaje, dos veces")
	c1, err := bc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	c2, err := bc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Equal(c1, c2) {
		t.Fatal("Encrypt con la misma clave y el mismo plaintext produjo el mismo ciphertext: el nonce no es aleatorio")
	}
	// Cada ciphertext empieza por un nonce distinto.
	if bytes.Equal(c1[:nonceSize], c2[:nonceSize]) {
		t.Fatal("Dos ciphertexts con el mismo plaintext compartieron nonce")
	}
}

func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	bc, err := NewBoardCrypto()
	if err != nil {
		t.Fatalf("NewBoardCrypto: %v", err)
	}
	plaintext := []byte("contenido secreto del tablero")
	ciphertext, err := bc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// Modifica un byte del cuerpo cifrado (después del nonce).
	ciphertext[nonceSize] ^= 0xFF
	if _, err := bc.Decrypt(ciphertext); err == nil {
		t.Fatal("Decrypt aceptó un ciphertext manipulado: falta comprobación de autenticidad")
	}
}

func TestDecryptRejectsWrongKey(t *testing.T) {
	alice, err := NewBoardCrypto()
	if err != nil {
		t.Fatalf("NewBoardCrypto: %v", err)
	}
	bob, err := NewBoardCrypto()
	if err != nil {
		t.Fatalf("NewBoardCrypto: %v", err)
	}
	if bytes.Equal(alice.PublicKey(), bob.PublicKey()) {
		t.Fatal("NewBoardCrypto generó la misma clave dos veces")
	}
	plaintext := []byte("solo para Alice")
	ciphertext, err := alice.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := bob.Decrypt(ciphertext); err == nil {
		t.Fatal("Bob descifró un mensaje cifrado con la clave de Alice: las claves no están aisladas")
	}
}

func TestPeerKeyEnablesSharedDecryption(t *testing.T) {
	alice, err := NewBoardCrypto()
	if err != nil {
		t.Fatalf("NewBoardCrypto: %v", err)
	}
	bob, err := NewBoardCrypto()
	if err != nil {
		t.Fatalf("NewBoardCrypto: %v", err)
	}
	// Bob recibe la clave del board de Alice a través del invite code y la fija
	// como clave del peer, de modo que ambos compartan la misma clave simétrica.
	bob.SetPeerKey(alice.PublicKey())
	plaintext := []byte("clave compartida vía invite code")
	ciphertext, err := alice.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	decrypted, err := bob.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(plaintext, decrypted) {
		t.Fatalf("Decrypt = %q, quiere %q", decrypted, plaintext)
	}
}

func TestGenerateInviteCodeKey(t *testing.T) {
	key := GenerateInviteCodeKey()
	if key == "" {
		t.Fatal("GenerateInviteCodeKey devolvió una cadena vacía")
	}
	// 32 bytes en base64 URL-safe sin padding ocupan 43 caracteres.
	const wantLen = 43
	if len(key) != wantLen {
		t.Fatalf("GenerateInviteCodeKey = %q (len %d), quiere len %d", key, len(key), wantLen)
	}
	if strings.ContainsAny(key, "+/") || strings.Contains(key, "=") {
		t.Fatalf("GenerateInviteCodeKey = %q no es base64 URL-safe", key)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil {
		t.Fatalf("base64.RawURLEncoding.DecodeString(GenerateInviteCodeKey()): %v", err)
	}
	if len(decoded) != keySize {
		t.Fatalf("clave decodificada = %d bytes, quiere %d", len(decoded), keySize)
	}
	// Dos llamadas deben producir claves distintas (aleatorias).
	if GenerateInviteCodeKey() == key {
		t.Fatal("GenerateInviteCodeKey produjo la misma clave dos veces")
	}
}

// La prueba de un claim depende del código y de cada campo que liga: otro
// código, otro dominio u otro campo producen otra prueba, y el código nunca
// aparece en ella.
func TestProofBindsKeyDomainAndFields(t *testing.T) {
	key := InviteCodeClaimKey("bunker-invite-abc")
	p := Proof(key, "member.claim", "board-1", "member-1")
	if p == "" || strings.Contains(p, "bunker-invite-abc") {
		t.Fatalf("Proof = %q", p)
	}
	if p != Proof(key, "member.claim", "board-1", "member-1") {
		t.Fatal("Proof no es determinista")
	}
	for name, other := range map[string]string{
		"otro código":     Proof(InviteCodeClaimKey("bunker-invite-xyz"), "member.claim", "board-1", "member-1"),
		"otro dominio":    Proof(key, "member.join", "board-1", "member-1"),
		"otro campo":      Proof(key, "member.claim", "board-2", "member-1"),
		"campos corridos": Proof(key, "member.claim", "board-1member-1"),
	} {
		if other == p {
			t.Errorf("%s produce la misma prueba", name)
		}
	}
	if !ProofEqual(p, Proof(key, "member.claim", "board-1", "member-1")) || ProofEqual(p, "") {
		t.Fatal("ProofEqual no compara bien")
	}
}
