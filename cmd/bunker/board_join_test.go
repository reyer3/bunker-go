package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/relay"
)

// La invitación que se comparte lleva el código y el board, y ya no la
// clave del board: esa llega cifrada en el welcome, solo a quien prueba el
// código ante el creador.
func TestInviteCodeStringCarriesBoardNotKey(t *testing.T) {
	inv := &core.InviteCode{Code: "bunker-invite-abc", BoardID: "id-board", PublicKey: []byte("clave-secreta-del-board-32-bytes")}
	got := inviteCodeString(inv)
	if got != "bunker-invite-abc@id-board" {
		t.Fatalf("inviteCodeString = %q", got)
	}
	code, boardID := splitInviteCode(got)
	if code != "bunker-invite-abc" || boardID != "id-board" {
		t.Fatalf("splitInviteCode = %q, %q", code, boardID)
	}
}

// --dry-run devuelve el plan del claim y no toca la red ni el daemon.
func TestCmdBoardJoinDryRunPlansWithoutNetwork(t *testing.T) {
	t.Setenv("BUNKER_RELAY_ADDR", "127.0.0.1:1")
	backend := newFakeBackend()
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	code := cmdBoardJoin(context.Background(), backend, []string{"bunker-invite-abc@id-board", "--member-id", "m1", "--dry-run", "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, stderr.String())
	}
	var out struct {
		DryRun bool      `json:"dryRun"`
		Plan   core.Plan `json:"plan"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &out); err != nil {
		t.Fatalf("json: %v (%q)", err, stdout.String())
	}
	if !out.DryRun || out.Plan.Action != "board.join" || out.Plan.Target != "id-board" || !strings.Contains(out.Plan.Preview, "127.0.0.1:1") {
		t.Fatalf("plan = %+v", out)
	}
	if strings.Contains(stdout.String(), "bunker-invite-abc") {
		t.Fatalf("el plan muestra el código: %s", stdout.String())
	}
	if len(backend.boardClaimPeerCalls) != 0 || len(backend.boardJoinCalls) != 0 {
		t.Fatal("--dry-run llamó al backend")
	}
}

// Sin relay, el claim va por el nodo P2P del daemon local.
func TestCmdBoardJoinClaimsOverP2PWithoutRelay(t *testing.T) {
	t.Setenv("BUNKER_RELAY_ADDR", "")
	backend := newFakeBackend()
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	if code := cmdBoardJoin(context.Background(), backend, []string{"bunker-invite-abc@id-board", "--member-id", "m1", "--name", "Carla"}, stdout, stderr); code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, stderr.String())
	}
	if len(backend.boardClaimPeerCalls) != 1 {
		t.Fatalf("boardClaimPeerCalls = %d", len(backend.boardClaimPeerCalls))
	}
	got := backend.boardClaimPeerCalls[0]
	if got.Code != "bunker-invite-abc" || got.BoardID != "id-board" || got.MemberID != "m1" || got.Name != "Carla" {
		t.Fatalf("call = %+v", got)
	}
}

// Con --relay, el CLI reclama contra el relay del creador y el daemon
// local adopta el welcome; un código ajeno falla ruidoso con exit != 0.
func TestCmdBoardJoinClaimsOverRelay(t *testing.T) {
	creator, joiner := newClaimDevice(t), newClaimDevice(t)
	b, inv := creatorWithInvite(t, creator)
	r := relay.NewRelay("127.0.0.1:0")
	r.SetAdmission(boardRelayAdmission(creator.svc))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	backend := daemonBackend{joiner.svc}

	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	bad := "bunker-invite-otro@" + b.ID
	if code := cmdBoardJoin(ctx, backend, []string{bad, "--relay", r.Addr().String(), "--member-id", "m1"}, stdout, stderr); code == 0 {
		t.Fatal("un código ajeno no hizo fallar el join")
	}
	if !strings.Contains(stderr.String(), "rechazado") {
		t.Fatalf("stderr = %q, want el motivo del rechazo", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	full := inviteCodeString(inv)
	if code := cmdBoardJoin(ctx, backend, []string{full, "--relay", r.Addr().String(), "--member-id", "m1"}, stdout, stderr); code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "m1" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if _, err := joiner.st.GetBoard(b.ID); err != nil {
		t.Fatalf("el daemon local no adoptó el board: %v", err)
	}
}
