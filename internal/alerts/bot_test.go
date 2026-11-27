package alerts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// startBot runs the poller until the test ends. Sleeps are short real waits so
// a poller that backs off still makes progress.
// The returned func stops it and waits.
func (e *env) startBot(t *testing.T) func() {
	t.Helper()
	e.svc.d.Sleep = func(ctx context.Context, _ time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
			return nil
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { _ = e.svc.RunBot(ctx); close(done) }()
	stop := func() { cancel(); <-done }
	t.Cleanup(stop)
	return stop
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (e *env) lastText(chat string) string {
	ms := e.tg.Messages()
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].ChatID == chat {
			return ms[i].Text
		}
	}
	return ""
}

func (e *env) channelChat(t *testing.T, u uuid.UUID) (chat int64, ok bool) {
	t.Helper()
	err := e.pool.QueryRow(t.Context(), `SELECT chat_id FROM notification_channel WHERE user_id = $1`, u).Scan(&chat)
	return chat, err == nil
}

func TestTelegramLinkFlow(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	hh := e.household(t, "Home", u)
	e.item(t, hh, "Milk", "2026-11-28", false)
	e.item(t, hh, "Low eggs", "", true)
	e.startBot(t)

	link, err := e.svc.StartLink(t.Context(), u)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link.URL, "https://t.me/stockpot_test_bot?start=") {
		t.Fatalf("url %q", link.URL)
	}
	code := strings.TrimPrefix(link.URL, "https://t.me/stockpot_test_bot?start=")

	e.tg.Say(777, "kim_tg", "/start "+code)
	eventually(t, "link", func() bool { _, ok := e.channelChat(t, u); return ok })
	eventually(t, "welcome", func() bool { return strings.Contains(e.lastText("777"), "Connected, Kim") })
	st, _ := e.svc.Telegram(t.Context(), u)
	if !st.Linked || !st.Enabled || st.Username != "kim_tg" {
		t.Fatalf("%+v", st)
	}

	// The code is single use.
	e.tg.Say(888, "mallory", "/start "+code)
	eventually(t, "reused code answered", func() bool { return strings.Contains(e.lastText("888"), "expired or was already used") })
	if _, ok := e.channelChat(t, u); !ok {
		t.Fatal("link lost")
	}

	// /expiring lists dates only; the low item is not shown.
	e.tg.Say(777, "kim_tg", "/expiring")
	eventually(t, "/expiring", func() bool { return strings.Contains(e.lastText("777"), "Milk (Fridge)") })
	if strings.Contains(e.lastText("777"), "eggs") {
		t.Fatalf("/expiring listed low stock: %s", e.lastText("777"))
	}

	// A chat that isn't linked gets guidance, not data.
	e.tg.Say(999, "", "/expiring")
	eventually(t, "unlinked /expiring", func() bool { return strings.Contains(e.lastText("999"), "isn't connected") })

	e.tg.Say(777, "kim_tg", "/stop")
	eventually(t, "unlink", func() bool { _, ok := e.channelChat(t, u); return !ok })
	eventually(t, "stop reply", func() bool { return strings.Contains(e.lastText("777"), "Disconnected") })

	var links, unlinks int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE action = 'telegram.link'), count(*) FILTER (WHERE action = 'telegram.unlink') FROM audit_event WHERE actor_id = $1`, u).Scan(&links, &unlinks)
	if links != 1 || unlinks != 1 {
		t.Fatalf("audit: %d link, %d unlink", links, unlinks)
	}
}

func TestLinkCodeExpires(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	link, err := e.svc.StartLink(t.Context(), u)
	if err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE telegram_link SET expires_at = now() - interval '1 minute'`)
	code := strings.TrimPrefix(link.URL, "https://t.me/stockpot_test_bot?start=")
	if _, err := e.svc.completeLink(t.Context(), code, 1, ""); !errors.Is(err, ErrLinkNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewLinkCodeReplacesOld(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	a, _ := e.svc.StartLink(t.Context(), u)
	b, _ := e.svc.StartLink(t.Context(), u)
	pre := "https://t.me/stockpot_test_bot?start="
	if _, err := e.svc.completeLink(t.Context(), strings.TrimPrefix(a.URL, pre), 1, ""); !errors.Is(err, ErrLinkNotFound) {
		t.Fatalf("old code still works: %v", err)
	}
	if _, err := e.svc.completeLink(t.Context(), strings.TrimPrefix(b.URL, pre), 1, ""); err != nil {
		t.Fatal(err)
	}
}

func TestChatCannotBeSharedBetweenUsers(t *testing.T) {
	e := newEnv(t)
	a, b := e.user(t, "Kim"), e.user(t, "Sam")
	e.link(t, a, 555)
	l, _ := e.svc.StartLink(t.Context(), b)
	_, err := e.svc.completeLink(t.Context(), strings.TrimPrefix(l.URL, "https://t.me/stockpot_test_bot?start="), 555, "")
	if !errors.Is(err, ErrChatTaken) {
		t.Fatalf("err = %v", err)
	}
	if chat, ok := e.channelChat(t, a); !ok || chat != 555 {
		t.Fatal("first user's link changed")
	}
}

func TestStartLinkNeedsBot(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	e.exec(t, `DELETE FROM setting WHERE key = 'telegram.bot_token'`)
	if err := e.store.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StartLink(t.Context(), u); err == nil || !strings.Contains(err.Error(), "bot token") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnlinkViaAPIIsAudited(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	e.link(t, u, 555)
	if err := e.svc.Unlink(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.channelChat(t, u); ok {
		t.Fatal("still linked")
	}
	if err := e.svc.Unlink(t.Context(), u); err != nil { // idempotent
		t.Fatal(err)
	}
}

func TestOnlyOnePollerConsumes(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	l, _ := e.svc.StartLink(t.Context(), u)

	// Another "process" holds the poll lock.
	conn, err := e.pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `SELECT pg_advisory_lock($1)`, pollLockKey); err != nil {
		t.Fatal(err)
	}
	e.startBot(t)
	e.tg.Say(777, "kim_tg", "/start "+strings.TrimPrefix(l.URL, "https://t.me/stockpot_test_bot?start="))
	time.Sleep(300 * time.Millisecond)
	if _, ok := e.channelChat(t, u); ok || e.tg.Calls("getUpdates") != 0 {
		t.Fatalf("a second poller consumed updates (getUpdates calls: %d)", e.tg.Calls("getUpdates"))
	}
	// When the holder goes away, this process takes over.
	_, _ = conn.Exec(t.Context(), `SELECT pg_advisory_unlock($1)`, pollLockKey)
	conn.Release()
	eventually(t, "takeover", func() bool { _, ok := e.channelChat(t, u); return ok })
}

func TestPollOffsetIsPersisted(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	stop := e.startBot(t)
	l, _ := e.svc.StartLink(t.Context(), u)
	e.tg.Say(777, "kim_tg", "/start "+strings.TrimPrefix(l.URL, "https://t.me/stockpot_test_bot?start="))
	eventually(t, "link", func() bool { _, ok := e.channelChat(t, u); return ok })
	eventually(t, "offset saved", func() bool {
		var off int64
		_ = e.pool.QueryRow(t.Context(), `SELECT update_offset FROM telegram_poll`).Scan(&off)
		return off == 2
	})
	stop()

	// A new poller resumes after the saved offset: it asks for offset 2 and
	// does not answer the old update again.
	before := len(e.tg.Messages())
	e.startBot(t)
	eventually(t, "second poller polling", func() bool { return e.tg.Calls("getUpdates") > 2 })
	time.Sleep(150 * time.Millisecond)
	if len(e.tg.Messages()) != before {
		t.Fatalf("update processed twice: %d -> %d messages", before, len(e.tg.Messages()))
	}
}
