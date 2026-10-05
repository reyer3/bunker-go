package core_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// awaitingStore is a fake core.AwaitingLister: it records the query and
// returns its canned items, as the SQLite store would after filtering.
type awaitingStore struct {
	*memStore
	asked core.AwaitingQuery
	out   []core.Item
}

func (s *awaitingStore) AwaitingReply(_ context.Context, q core.AwaitingQuery) ([]core.Item, error) {
	s.asked = q
	return s.out, nil
}

var awaitNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func awaitingService(out ...core.Item) (*core.Service, *awaitingStore) {
	st := &awaitingStore{memStore: newMemStore(), out: out}
	svc := core.NewService(st, core.NewRegistry())
	svc.SetQueryClock(func() time.Time { return awaitNow })
	return svc, st
}

func TestAwaitingReplyDefaultsAndQuery(t *testing.T) {
	svc, st := awaitingService()
	if _, err := svc.AwaitingReply(context.Background(), core.AwaitingFilter{}); err != nil {
		t.Fatal(err)
	}
	if want := awaitNow.Add(-3 * 24 * time.Hour); !st.asked.Before.Equal(want) {
		t.Errorf("before = %v, want %v (3 days)", st.asked.Before, want)
	}
	if st.asked.Limit <= 0 || st.asked.Groups {
		t.Errorf("query = %+v, want a positive limit and no groups", st.asked)
	}

	if _, err := svc.AwaitingReply(context.Background(), core.AwaitingFilter{Days: 7, Channel: core.ChannelMail,
		Account: "cl", Groups: true, Limit: 5}); err != nil {
		t.Fatal(err)
	}
	q := st.asked
	if !q.Before.Equal(awaitNow.Add(-7*24*time.Hour)) || q.Channel != core.ChannelMail || q.Account != "cl" || !q.Groups || q.Limit != 5 {
		t.Errorf("query = %+v", q)
	}
}

func TestAwaitingReplyRows(t *testing.T) {
	chat := core.Item{ID: "whatsapp:personal:A9", Channel: core.ChannelWhatsApp, Account: "personal",
		Thread: "51900000001@s.whatsapp.net", ThreadName: "Ana Ejemplo", FromMe: true,
		Body:      "te paso el\n  informe   cuando lo tenga listo, avísame si necesitas algo más de mi parte por favor",
		Timestamp: awaitNow.Add(-4*24*time.Hour - time.Hour)}
	mail := core.Item{ID: "mail:cl:7", Channel: core.ChannelMail, Account: "cl", Thread: "t7", ThreadName: "Cotización",
		FromMe: true, Subject: "Cotización", Body: "Hola, adjunto la cotización",
		To:        []core.Address{{ID: "ventas@example.com", Name: "Proveedor Ejemplo"}},
		Timestamp: awaitNow.Add(-10 * 24 * time.Hour)}
	photo := core.Item{ID: "matrix:mx:3", Channel: core.ChannelMatrix, Account: "mx", Thread: "!room:example.org",
		ThreadName: "Luis", FromMe: true, Attachments: []core.Attachment{{Name: "plano.pdf"}},
		Timestamp: awaitNow.Add(-3 * 24 * time.Hour)}
	svc, _ := awaitingService(chat, mail, photo)

	got, err := svc.AwaitingReply(context.Background(), core.AwaitingFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("rows = %+v", got)
	}
	a := got[0]
	if a.ItemID != chat.ID || a.Channel != core.ChannelWhatsApp || a.Account != "personal" || a.Thread != chat.Thread ||
		a.Person != "Ana Ejemplo" || !a.Sent.Equal(chat.Timestamp) || a.Days != 4 {
		t.Errorf("chat row = %+v", a)
	}
	if !strings.HasPrefix(a.Preview, "te paso el informe cuando") || !strings.HasSuffix(a.Preview, "…") ||
		len([]rune(a.Preview)) > core.AwaitingPreviewLen+1 {
		t.Errorf("preview = %q, want whitespace collapsed and cut", a.Preview)
	}
	if m := got[1]; m.Person != "Proveedor Ejemplo" || m.Preview != "Cotización" || m.Days != 10 {
		t.Errorf("mail row = %+v, want the recipient and the subject", m)
	}
	if p := got[2]; p.Preview != "plano.pdf" || p.Days != 3 {
		t.Errorf("attachment row = %+v, want the file name as preview", p)
	}
}

func TestAwaitingReplyUnsupportedStore(t *testing.T) {
	svc := core.NewService(newMemStore(), core.NewRegistry())
	if _, err := svc.AwaitingReply(context.Background(), core.AwaitingFilter{}); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestIsGroupConversation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		channel core.Channel
		thread  string
		others  int
		want    bool
	}{
		{"whatsapp group", core.ChannelWhatsApp, "120363000000000001@g.us", 0, true},
		{"whatsapp broadcast list", core.ChannelWhatsApp, "1700000000@broadcast", 0, true},
		{"whatsapp channel", core.ChannelWhatsApp, "120363000000000002@newsletter", 0, true},
		{"whatsapp one to one", core.ChannelWhatsApp, "51900000001@s.whatsapp.net", 5, false},
		{"whatsapp lid one to one", core.ChannelWhatsApp, "100000000000001@lid", 1, false},
		{"matrix room with two others", core.ChannelMatrix, "!room:example.org", 2, true},
		{"matrix room with one other", core.ChannelMatrix, "!dm:example.org", 1, false},
		{"matrix room nobody answered", core.ChannelMatrix, "!new:example.org", 0, false},
		{"mail thread", core.ChannelMail, "t1", 9, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := core.IsGroupConversation(tc.channel, tc.thread, tc.others); got != tc.want {
				t.Errorf("IsGroupConversation = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsSelfChat(t *testing.T) {
	for _, tc := range []struct {
		name    string
		channel core.Channel
		thread  string
		from    string
		want    bool
	}{
		{"own number from another device", core.ChannelWhatsApp, "51900000009@s.whatsapp.net", "51900000009:12@s.whatsapp.net", true},
		{"own number", core.ChannelWhatsApp, "51900000009@s.whatsapp.net", "51900000009@s.whatsapp.net", true},
		{"someone else", core.ChannelWhatsApp, "51900000001@s.whatsapp.net", "51900000009:12@s.whatsapp.net", false},
		{"same user other server", core.ChannelWhatsApp, "51900000009@lid", "51900000009@s.whatsapp.net", false},
		{"empty sender", core.ChannelWhatsApp, "51900000009@s.whatsapp.net", "", false},
		{"matrix", core.ChannelMatrix, "!room:example.org", "@me:example.org", false},
		{"mail", core.ChannelMail, "t1", "t1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := core.IsSelfChat(tc.channel, tc.thread, tc.from); got != tc.want {
				t.Errorf("IsSelfChat = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAwaitingReplyPassesMail(t *testing.T) {
	svc, st := awaitingService()
	if _, err := svc.AwaitingReply(context.Background(), core.AwaitingFilter{}); err != nil {
		t.Fatal(err)
	}
	if st.asked.Mail {
		t.Errorf("default query = %+v, want Mail off", st.asked)
	}
	if _, err := svc.AwaitingReply(context.Background(), core.AwaitingFilter{Mail: true}); err != nil {
		t.Fatal(err)
	}
	if !st.asked.Mail {
		t.Errorf("query = %+v, want Mail on", st.asked)
	}
}

func TestAsksQuestion(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"spanish question", "Hola Ana,\n\n¿Me confirmas la reunión del jueves?\n\nSaludos", true},
		{"opening mark only", "¿Te llegó el archivo", true},
		{"english question", "Hi,\n\nCould you send the report by Friday?\n\nThanks", true},
		{"informative spanish", "Hola,\n\nTe envié el archivo con la factura.\n\nSaludos", false},
		{"informative english", "Please find the invoice attached.", false},
		{"empty", "", false},
		{"question only in quoted lines", "Listo, adjunto.\n\n> ¿Me mandas la factura?\n> Gracias", false},
		{"question after spanish attribution", "Te envié el archivo.\n\nEl lun, 5 oct 2026 a las 10:00, Ana Ejemplo <ana@example.com> escribió:\n¿Me lo mandas?", false},
		{"wrapped spanish attribution", "Adjunto la factura.\n\nEl lun, 5 oct 2026 a las 10:00, Ana Ejemplo\n<ana@example.com> escribió:\n¿Me la mandas?", false},
		{"question after english attribution", "Here it is.\n\nOn Mon, Oct 5, 2026 at 10:00 AM Ana Example <ana@example.com> wrote:\nCan you send it?", false},
		{"bunker quote", "Done.\n\nOn 2026-10-05 10:00 UTC, ana@example.com wrote:\n> Can you send it?", false},
		{"original message separator", "Adjunto.\n\n-----Original Message-----\nFrom: Ana\nIs it ready?", false},
		{"mensaje original separator", "Adjunto.\n\n-----Mensaje original-----\n¿Está listo?", false},
		{"outlook spanish header block", "Te envié el archivo.\n\nDe: Ana Ejemplo <ana@example.com>\nEnviado: lunes, 5 de octubre de 2026 10:00\nPara: Yo\nAsunto: ¿Factura?\n\n¿Me la mandas?", false},
		{"outlook english header block", "Sent it.\n\nFrom: Ana Example\nSent: Monday, October 5, 2026 10:00 AM\nTo: Me\nSubject: Report?\n\nAny news?", false},
		{"question before the quote", "¿Lo revisaste?\n\nOn Mon, Oct 5, 2026 at 10:00 AM Ana <ana@example.com> wrote:\n> Te lo envío", true},
		{"from line of my own text", "De: mi parte, gracias por todo.\nNos vemos el lunes.", false},
		{"from line before a question", "De: mi parte, gracias.\n¿Nos vemos el lunes?", true},
		{"link with a query string", "El enlace: https://example.com/doc?id=7&v=2", false},
		{"signature with a question", "Adjunto el informe.\n\n-- \n¿Dudas? Llámame", false},
		{"windows line endings", "Hola\r\n¿Vienes mañana?\r\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := core.AsksQuestion(tc.body); got != tc.want {
				t.Errorf("AsksQuestion(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func TestUsableThreadName(t *testing.T) {
	for _, tc := range []struct {
		name, thread, threadName string
		want                     bool
	}{
		{"contact name", "51900000001@s.whatsapp.net", "Ana Ejemplo", true},
		{"empty", "51900000001@s.whatsapp.net", "", false},
		{"blank", "51900000001@s.whatsapp.net", "  ", false},
		{"bare number", "51900000001@s.whatsapp.net", "51900000001", false},
		{"formatted number", "51900000001@s.whatsapp.net", "+51 900 000-001", false},
		{"lid digits", "51900000001@s.whatsapp.net", "100000000000001", false},
		{"the thread id itself", "!room:example.org", "!room:example.org", false},
		{"name with digits", "51900000001@s.whatsapp.net", "Ana 2", true},
		{"matrix room name", "!room:example.org", "Proyecto", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := core.UsableThreadName(tc.thread, tc.threadName); got != tc.want {
				t.Errorf("UsableThreadName(%q, %q) = %v, want %v", tc.thread, tc.threadName, got, tc.want)
			}
		})
	}
}
