// Package board implementa el cifrado de extremo a extremo (E2EE) para los
// datos de los tableros compartidos. Los contenidos de un tablero (tareas,
// comentarios, ...) se cifran con una clave simétrica AES-256-GCM que nunca
// sale de los dispositivos de los miembros: el relay solo almacena
// ciphertext y no puede descifrarlo.
package board

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

const (
	// keySize es el tamaño de la clave simétrica AES-256, en bytes.
	keySize = 32
	// nonceSize es el tamaño del nonce de AES-GCM, en bytes (96 bits).
	nonceSize = 12
)

// BoardCrypto cifra y descifra datos de un tablero compartido con
// AES-256-GCM. Cada BoardCrypto lleva una clave simétrica de 32 bytes. La
// clave se genera al crear el tablero y se comparte con cada miembro a
// través del invite code; el relay nunca la recibe.
type BoardCrypto struct {
	key  []byte // clave simétrica propia (32 bytes)
	peer []byte // clave del peer para cifrado compartido; cuando se fija, reemplaza a key
}

// NewBoardCrypto genera una clave simétrica aleatoria (AES-256) y devuelve
// un BoardCrypto listo para cifrar y descifrar datos de tablero.
func NewBoardCrypto() (*BoardCrypto, error) {
	key := make([]byte, keySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("board: generar clave simétrica: %w", err)
	}
	return &BoardCrypto{key: key}, nil
}

// activeKey devuelve la clave usada para cifrar y descifrar: la del peer si
// se ha fijado una con SetPeerKey, o la propia del board en otro caso.
func (bc *BoardCrypto) activeKey() []byte {
	if len(bc.peer) == keySize {
		return bc.peer
	}
	return bc.key
}

// Encrypt cifra plaintext con AES-256-GCM y devuelve el nonce (12 bytes)
// concatenado con el ciphertext: nonce || ciphertext. El nonce es aleatorio
// en cada llamada, así que cifrar dos veces el mismo plaintext produce
// ciphertexts diferentes.
func (bc *BoardCrypto) Encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(bc.activeKey())
	if err != nil {
		return nil, fmt.Errorf("board: crear cifrador AES: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("board: crear GCM: %w", err)
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("board: generar nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	return append(nonce, ciphertext...), nil
}

// Decrypt descifra ciphertext (nonce || ciphertext) producido por Encrypt y
// verifica su autenticidad. Devuelve el plaintext o un error si el ciphertext
// fue manipulado, está corrupto o se cifra con otra clave.
func (bc *BoardCrypto) Decrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < nonceSize {
		return nil, errors.New("board: ciphertext demasiado corto para contener el nonce")
	}
	block, err := aes.NewCipher(bc.activeKey())
	if err != nil {
		return nil, fmt.Errorf("board: crear cifrador AES: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("board: crear GCM: %w", err)
	}
	nonce, data := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return nil, errors.New("board: descifrado falló: comprobación de autenticidad no válida")
	}
	return plaintext, nil
}

// PublicKey devuelve una copia de la clave simétrica del board, para
// compartirla con los miembros a través del invite code. El relay nunca la
// recibe, por tanto no puede descifrar el contenido del tablero.
func (bc *BoardCrypto) PublicKey() []byte {
	out := make([]byte, len(bc.key))
	copy(out, bc.key)
	return out
}

// SetPeerKey establece la clave de un peer para cifrado compartido. Una vez
// establecida, Encrypt y Decrypt usan la clave del peer en lugar de la propia
// del board, de modo que dos miembros del mismo tablero usen la misma clave
// compartida recibida vía invite code.
func (bc *BoardCrypto) SetPeerKey(key []byte) {
	bc.peer = make([]byte, len(key))
	copy(bc.peer, key)
}

// GenerateInviteCodeKey genera una clave de invite code legible: 32 bytes
// aleatorios codificados en base64 URL-safe (sin padding). Puede incrustarse
// en un invite code para compartir la clave del board con un nuevo miembro.
func GenerateInviteCodeKey() string {
	key := make([]byte, keySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		// crypto/rand no falla en la práctica; si falla es un error fatal.
		panic(fmt.Errorf("board: generar clave de invite code: %w", err))
	}
	return base64.RawURLEncoding.EncodeToString(key)
}
