package alerts

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/settings"
	"github.com/a-jay98/stockpot/internal/telegram"
	"github.com/a-jay98/stockpot/internal/telegram/telegramtest"
	"github.com/a-jay98/stockpot/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

type env struct {
	pool  *pgxpool.Pool
	store *settings.Store
	tg    *telegramtest.Server
	svc   *Service

	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (e *env) setNow(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	e.now = t
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	store, err := settings.NewStore(pool, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.SetValue(t.Context(), store, telegram.SettingBotToken, telegramtest.Token, nil); err != nil {
		t.Fatal(err)
	}
	tg := telegramtest.New(t)
	e := &env{pool: pool, store: store, tg: tg}
	e.setNow("2026-11-27T13:00:00Z") // 08:00 in Toronto (EST)
	e.svc = NewService(Deps{
		Pool: pool, Settings: store,
		Telegram:  telegram.New(func() string { return telegram.SettingBotToken.Get(store) }, telegram.WithBaseURL(tg.URL)),
		PublicURL: "https://stockpot.example",
		Now: func() time.Time {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.now
		},
		Sleep: func(ctx context.Context, d time.Duration) error {
			e.mu.Lock()
			e.sleeps = append(e.sleeps, d)
			e.mu.Unlock()
			return ctx.Err()
		},
	})
	return e
}

func (e *env) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(t.Context(), q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) user(t *testing.T, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	e.exec(t, `INSERT INTO app_user (id, display_name, status) VALUES ($1, $2, 'active')`, id, name)
	return id
}

func (e *env) household(t *testing.T, name string, users ...uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	e.exec(t, `INSERT INTO household (id, name) VALUES ($1, $2)`, id, name)
	for _, u := range users {
		e.exec(t, `INSERT INTO membership (user_id, household_id, role) VALUES ($1, $2, 'member')`, u, id)
	}
	return id
}

// item adds a stock item in the household's fridge. expires may be "".
func (e *env) item(t *testing.T, hh uuid.UUID, name, expires string, low bool) uuid.UUID {
	t.Helper()
	product := uuid.New()
	e.exec(t, `INSERT INTO product (id, household_id, name) VALUES ($1, $2, $3)`, product, hh, name)
	id := uuid.New()
	var exp any
	if expires != "" {
		exp = expires
	}
	e.exec(t, `INSERT INTO stock_item (id, household_id, product_id, location_id, quantity, unit, expires_on, low)
		VALUES ($1, $2, $3, (SELECT id FROM location WHERE household_id = $2 AND kind = 'fridge'), 1, 'pc', $4::date, $5)`,
		id, hh, product, exp, low)
	return id
}

func (e *env) link(t *testing.T, user uuid.UUID, chat int64) {
	t.Helper()
	e.exec(t, `INSERT INTO notification_channel (user_id, kind, chat_id, username, linked_at)
		VALUES ($1, 'telegram', $2, 'kim', '2026-01-01')`, user, chat)
}

func (e *env) notifications(t *testing.T, user uuid.UUID) []Notification {
	t.Helper()
	items, _, err := e.svc.List(t.Context(), user, ListParams{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestDigestSelectsByDateBoundaries(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	hh := e.household(t, "Home", u)
	other := e.household(t, "Other", e.user(t, "Sam"))

	// Today is 2026-11-27, lead days 2.
	e.item(t, hh, "Old milk", "2026-11-26", false)
	e.item(t, hh, "Today yogurt", "2026-11-27", false)
	e.item(t, hh, "Edge cheese", "2026-11-29", false)
	e.item(t, hh, "Beyond ham", "2026-11-30", false)
	e.item(t, hh, "Low eggs", "", true)
	e.item(t, hh, "No date", "", false)
	gone := e.item(t, hh, "Deleted butter", "2026-11-27", false)
	e.exec(t, `UPDATE stock_item SET deleted_at = now() WHERE id = $1`, gone)
	empty := e.item(t, hh, "Finished jam", "2026-11-27", false)
	e.exec(t, `UPDATE stock_item SET quantity = 0 WHERE id = $1`, empty)
	e.item(t, other, "Other household", "2026-11-27", false)

	if err := e.svc.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	ns := e.notifications(t, u)
	if len(ns) != 1 {
		t.Fatalf("notifications = %d", len(ns))
	}
	n := ns[0]
	if n.Kind != KindExpiryDigest || n.HouseholdID != hh || n.Title != "1 expired · 2 expiring soon · 1 running low" {
		t.Fatalf("%+v", n)
	}
	var p Digest
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Expired) != 1 || p.Expired[0].Name != "Old milk" || len(p.Expiring) != 2 || p.Expiring[1].Name != "Edge cheese" || len(p.Low) != 1 {
		t.Fatalf("payload %+v", p)
	}
	if strings.Contains(n.Body, "Beyond") || strings.Contains(n.Body, "Other household") || strings.Contains(n.Body, "Deleted") || strings.Contains(n.Body, "Finished") {
		t.Fatalf("body %q", n.Body)
	}
}

func TestLeadDaysPreferenceOverridesDefault(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	hh := e.household(t, "Home", u)
	e.item(t, hh, "Far cheese", "2026-12-04", false) // +7 days
	if err := e.svc.SetPrefs(t.Context(), u, Prefs{DigestTime: "08:00", LeadDays: 7, QuietStart: "22:00", QuietEnd: "07:00"}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ns := e.notifications(t, u); len(ns) != 1 || !strings.Contains(ns[0].Body, "Far cheese") {
		t.Fatalf("%+v", ns)
	}
}

func TestDigestOncePerUserPerDay(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	hh := e.household(t, "Home", u)
	e.item(t, hh, "Milk", "2026-11-27", false)
	run := func(now string) {
		t.Helper()
		e.setNow(now)
		if err := e.svc.Run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	run("2026-11-27T12:59:00Z") // 07:59: not yet
	if n := len(e.notifications(t, u)); n != 0 {
		t.Fatalf("early digest: %d", n)
	}
	run("2026-11-27T13:00:00Z")
	run("2026-11-27T13:15:00Z") // later runs the same day add nothing
	run("2026-11-27T14:00:00Z")
	if n := len(e.notifications(t, u)); n != 1 {
		t.Fatalf("same day: %d", n)
	}
	run("2026-11-28T13:00:00Z") // next day, milk is now expired
	ns := e.notifications(t, u)
	if len(ns) != 2 || !strings.Contains(ns[0].Body, "Expired") {
		t.Fatalf("next day: %+v", ns)
	}
}

func TestEmptyDigestIsEvaluatedOnce(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	hh := e.household(t, "Home", u)
	if err := e.svc.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.item(t, hh, "Milk", "2026-11-27", false) // appears after the day's evaluation
	e.setNow("2026-11-27T13:30:00Z")
	if err := e.svc.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := len(e.notifications(t, u)); n != 0 {
		t.Fatalf("an empty day produced a late digest: %d", n)
	}
}

func TestDigestNotCreatedFarPastItsTime(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	hh := e.household(t, "Home", u)
	e.item(t, hh, "Milk", "2026-11-27", false)
	e.setNow("2026-11-27T23:00:00Z") // 18:00, ten hours late
	if err := e.svc.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := len(e.notifications(t, u)); n != 0 {
		t.Fatalf("stale digest: %d", n)
	}
}

func TestOneDigestPerHousehold(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	a, b := e.household(t, "Home", u), e.household(t, "Cabin", u)
	e.item(t, a, "Milk", "2026-11-27", false)
	e.item(t, b, "Beans", "2026-11-27", false)
	if err := e.svc.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := len(e.notifications(t, u)); n != 2 {
		t.Fatalf("notifications = %d", n)
	}
}

func TestDigestDisabledByFlagInWorker(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	hh := e.household(t, "Home", u)
	e.item(t, hh, "Milk", "2026-11-27", false)
	if err := settings.SetValue(t.Context(), e.store, FlagEnabled, false, nil); err != nil {
		t.Fatal(err)
	}
	w := &Worker{svc: e.svc}
	if err := w.Work(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if n := len(e.notifications(t, u)); n != 0 {
		t.Fatalf("flag off still created %d", n)
	}
}

func TestInboxReadAndPagination(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	other := e.user(t, "Sam")
	hh := e.household(t, "Home", u, other)
	for i, ts := range []string{"2026-11-25T13:00:00Z", "2026-11-26T13:00:00Z", "2026-11-27T13:00:00Z"} {
		e.exec(t, `INSERT INTO notification (user_id, household_id, kind, title, body, dedupe_key, created_at) VALUES ($1, $2, 'expiry_digest', $3, 'b', $3, $4)`, u, hh, "t"+string(rune('0'+i)), ts)
	}
	e.exec(t, `INSERT INTO notification (user_id, household_id, kind, title, body, dedupe_key) VALUES ($1, $2, 'expiry_digest', 'other', 'b', 'k')`, other, hh)

	page, unread, err := e.svc.List(t.Context(), u, ListParams{Limit: 2})
	if err != nil || unread != 3 || len(page) != 2 || page[0].Title != "t2" {
		t.Fatalf("%+v %d %v", page, unread, err)
	}
	older, _, _ := e.svc.List(t.Context(), u, ListParams{Limit: 2, Before: &page[1].CreatedAt})
	if len(older) != 1 || older[0].Title != "t0" {
		t.Fatalf("older %+v", older)
	}
	if err := e.svc.MarkRead(t.Context(), u, []uuid.UUID{page[0].ID}); err != nil {
		t.Fatal(err)
	}
	if _, unread, _ := e.svc.List(t.Context(), u, ListParams{}); unread != 2 {
		t.Fatalf("unread after one = %d", unread)
	}
	// Someone else's id is ignored; empty ids marks everything of mine.
	if err := e.svc.MarkRead(t.Context(), other, []uuid.UUID{page[1].ID}); err != nil {
		t.Fatal(err)
	}
	if _, unread, _ := e.svc.List(t.Context(), u, ListParams{}); unread != 2 {
		t.Fatalf("foreign mark changed my unread: %d", unread)
	}
	if err := e.svc.MarkRead(t.Context(), u, nil); err != nil {
		t.Fatal(err)
	}
	unreadOnly, unread, _ := e.svc.List(t.Context(), u, ListParams{UnreadOnly: true})
	if unread != 0 || len(unreadOnly) != 0 {
		t.Fatalf("after mark all: %d %d", unread, len(unreadOnly))
	}
	if _, o, _ := e.svc.List(t.Context(), other, ListParams{}); o != 1 {
		t.Fatalf("other's unread = %d", o)
	}
}

func TestPreferencesValidate(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	def, err := e.svc.GetPrefs(t.Context(), u)
	if err != nil || def.DigestTime != "08:00" || def.LeadDays != 2 || def.QuietStart != "22:00" || def.QuietEnd != "07:00" {
		t.Fatalf("defaults %+v %v", def, err)
	}
	for _, bad := range []Prefs{
		{DigestTime: "8:00", LeadDays: 2, QuietStart: "22:00", QuietEnd: "07:00"},
		{DigestTime: "08:00", LeadDays: 31, QuietStart: "22:00", QuietEnd: "07:00"},
		{DigestTime: "08:00", LeadDays: -1, QuietStart: "22:00", QuietEnd: "07:00"},
		{DigestTime: "08:00", LeadDays: 2, QuietStart: "25:00", QuietEnd: "07:00"},
	} {
		if err := e.svc.SetPrefs(t.Context(), u, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	want := Prefs{DigestTime: "06:45", LeadDays: 4, QuietStart: "23:00", QuietEnd: "06:00"}
	if err := e.svc.SetPrefs(t.Context(), u, want); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.GetPrefs(t.Context(), u); got != want {
		t.Fatalf("got %+v", got)
	}
}

func TestDeliverySendsOnceAndLinksBack(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	hh := e.household(t, "Home", u)
	e.item(t, hh, "Milk", "2026-11-27", false)
	e.link(t, u, 555)
	for i := 0; i < 2; i++ {
		if err := e.svc.Run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	msgs := e.tg.Messages()
	if len(msgs) != 1 || msgs[0].ChatID != "555" {
		t.Fatalf("messages %+v", msgs)
	}
	for _, want := range []string{"1 expiring soon", "• Milk (Fridge), today", "https://stockpot.example/inventory"} {
		if !strings.Contains(msgs[0].Text, want) {
			t.Errorf("message lacks %q:\n%s", want, msgs[0].Text)
		}
	}
	if n := e.notifications(t, u)[0]; n.ReadAt != nil {
		t.Error("sending to Telegram must not mark the inbox entry read")
	}
}

func TestQuietHoursHoldTelegramNotInbox(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	hh := e.household(t, "Home", u)
	e.item(t, hh, "Milk", "2026-11-27", false)
	e.link(t, u, 555)
	if err := e.svc.SetPrefs(t.Context(), u, Prefs{DigestTime: "08:00", LeadDays: 2, QuietStart: "07:00", QuietEnd: "09:00"}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Run(t.Context()); err != nil { // 08:00: quiet
		t.Fatal(err)
	}
	if len(e.notifications(t, u)) != 1 || len(e.tg.Messages()) != 0 {
		t.Fatalf("in quiet hours: inbox %d, telegram %d", len(e.notifications(t, u)), len(e.tg.Messages()))
	}
	e.setNow("2026-11-27T14:00:00Z") // 09:00: quiet is over
	if err := e.svc.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(e.tg.Messages()) != 1 {
		t.Fatalf("held message not sent after quiet hours: %d", len(e.tg.Messages()))
	}
}

func TestDeliveryRetriesWithBackoff(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	hh := e.household(t, "Home", u)
	e.item(t, hh, "Milk", "2026-11-27", false)
	e.link(t, u, 555)

	// A rate limit and a server error, then success: waits follow Telegram's
	// retry_after, then the exponential backoff.
	e.tg.Fail = func(method string, n int) (int, int) {
		switch {
		case method != "sendMessage":
			return 0, 0
		case n == 1:
			return 429, 3
		case n == 2:
			return 500, 0
		}
		return 0, 0
	}
	if err := e.svc.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := e.tg.Calls("sendMessage"); got != 3 || len(e.tg.Messages()) != 1 {
		t.Fatalf("sendMessage calls %d, delivered %d", got, len(e.tg.Messages()))
	}
	if len(e.sleeps) != 2 || e.sleeps[0] != 3*time.Second || e.sleeps[1] != 2*time.Second {
		t.Fatalf("waits %v", e.sleeps)
	}
}

func TestDeliveryGivesUpThenRetriesNextRun(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	hh := e.household(t, "Home", u)
	e.item(t, hh, "Milk", "2026-11-27", false)
	e.link(t, u, 555)
	down := true
	e.tg.Fail = func(method string, _ int) (int, int) {
		if method == "sendMessage" && down {
			return 502, 0
		}
		return 0, 0
	}
	if err := e.svc.Run(t.Context()); err != nil { // an outage is not a job failure
		t.Fatal(err)
	}
	if e.tg.Calls("sendMessage") != sendAttempts || len(e.tg.Messages()) != 0 {
		t.Fatalf("calls %d", e.tg.Calls("sendMessage"))
	}
	down = false
	e.setNow("2026-11-27T13:15:00Z")
	if err := e.svc.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(e.tg.Messages()) != 1 {
		t.Fatal("next run did not deliver")
	}
	// Past the delivery window nothing is sent any more.
	e2 := newEnv(t)
	u2 := e2.user(t, "Kim")
	hh2 := e2.household(t, "Home", u2)
	e2.item(t, hh2, "Milk", "2026-11-27", false)
	e2.link(t, u2, 556)
	e2.tg.Fail = func(m string, _ int) (int, int) { return map[bool]int{true: 502}[m == "sendMessage"], 0 }
	_ = e2.svc.Run(t.Context())
	e2.tg.Fail = nil
	e2.setNow("2026-11-28T14:00:00Z")
	e2.exec(t, `UPDATE digest_run SET day = day - 1`) // keep the new day's digest from existing
	if err := e2.svc.deliver(t.Context(), e2.now); err != nil {
		t.Fatal(err)
	}
	if len(e2.tg.Messages()) != 0 {
		t.Fatal("sent a notification older than the delivery window")
	}
}

func TestRefusedChatIsDisabled(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	hh := e.household(t, "Home", u)
	e.item(t, hh, "Milk", "2026-11-27", false)
	e.link(t, u, 555)
	e.tg.Fail = func(method string, _ int) (int, int) {
		if method == "sendMessage" {
			return 403, 0 // bot was blocked by the user
		}
		return 0, 0
	}
	if err := e.svc.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err := e.svc.Telegram(t.Context(), u)
	if err != nil || !st.Linked || st.Enabled || st.LastError == "" {
		t.Fatalf("%+v %v", st, err)
	}
	calls := e.tg.Calls("sendMessage")
	e.setNow("2026-11-27T13:15:00Z")
	_ = e.svc.Run(t.Context())
	if e.tg.Calls("sendMessage") != calls {
		t.Fatal("kept sending to a refused chat")
	}
}

func TestNotificationsBeforeLinkingAreNotSent(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "Kim")
	hh := e.household(t, "Home", u)
	e.item(t, hh, "Milk", "2026-11-27", false)
	if err := e.svc.Run(t.Context()); err != nil { // digest exists, no channel yet
		t.Fatal(err)
	}
	e.exec(t, `INSERT INTO notification_channel (user_id, kind, chat_id) VALUES ($1, 'telegram', 555)`, u) // linked now
	e.setNow("2026-11-27T13:15:00Z")
	if err := e.svc.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(e.tg.Messages()) != 0 {
		t.Fatal("sent a digest created before the chat was linked")
	}
}
