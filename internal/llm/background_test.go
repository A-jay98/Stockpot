package llm_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/a-jay98/stockpot/internal/llm"
	"github.com/a-jay98/stockpot/internal/settings"
)

func background(text string) llm.Request {
	r := ask(text)
	r.Priority = llm.Background
	return r
}

func TestBackgroundDeferredUnderReserve(t *testing.T) {
	a := newFake(t, ok("a", 1, 1))
	pa := provider("a", a)
	pa.DailyRequestCap = 10 // reserve 40% = 4 requests
	e := setup(t, pa)
	ctx := t.Context()

	// 6 interactive calls use the quota above the reserve line; 4 remain.
	for i := range 6 {
		if _, err := e.router.Complete(ctx, ask("i")); err != nil {
			t.Fatalf("interactive %d: %v", i, err)
		}
	}
	if ok, _ := e.router.Headroom(ctx, llm.Needs{}); ok {
		t.Fatal("Headroom ok with exactly the reserve left")
	}
	_, err := e.router.Complete(ctx, background("b"))
	if !errors.Is(err, llm.ErrDeferred) || !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("background err = %v, want ErrDeferred (an ErrUnavailable)", err)
	}
	if a.hits.Load() != 6 {
		t.Fatalf("deferred request reached the provider (%d hits)", a.hits.Load())
	}
	// Interactive requests are unaffected until the cap itself.
	for i := range 4 {
		if _, err := e.router.Complete(ctx, ask("i")); err != nil {
			t.Fatalf("interactive after deferral %d: %v", i, err)
		}
	}
	if _, err := e.router.Complete(ctx, ask("i")); !errors.Is(err, llm.ErrUnavailable) || errors.Is(err, llm.ErrDeferred) {
		t.Fatalf("at the cap: %v", err)
	}
}

func TestBackgroundRunsAboveReserveAndHeadroomSaysSo(t *testing.T) {
	a := newFake(t, ok("a", 1, 1))
	pa := provider("a", a)
	pa.DailyRequestCap = 10
	e := setup(t, pa)
	ctx := t.Context()

	if ok, _ := e.router.Headroom(ctx, llm.Needs{}); !ok {
		t.Fatal("no headroom on a fresh day")
	}
	for i := range 6 { // 4 left: exactly the reserve
		if _, err := e.router.Complete(ctx, background("b")); err != nil {
			t.Fatalf("background %d: %v", i, err)
		}
	}
	ok, retry := e.router.Headroom(ctx, llm.Needs{})
	if ok || !retry.After(e.clock.now()) {
		t.Fatalf("Headroom = %v, %v; want false and the next reset", ok, retry)
	}
	// Lowering the reserve opens headroom again; the new day resets it.
	if err := settings.SetValue(ctx, e.store, llm.SettingBackgroundReservePct, 10, nil); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.router.Headroom(ctx, llm.Needs{}); !ok {
		t.Fatal("no headroom with a 10% reserve and 4 of 10 left")
	}
	if _, err := e.router.Complete(ctx, background("b")); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundFailsOverToProviderWithHeadroom(t *testing.T) {
	a, b := newFake(t, ok("a", 1, 1)), newFake(t, ok("b", 1, 1))
	pa, pb := provider("a", a), provider("b", b)
	pa.DailyRequestCap = 3 // reserve 40% of 3 = 1.2: a serves while 3 then 2 are left, not 1
	e := setup(t, pa, pb)
	ctx := t.Context()
	var got []string
	for range 4 {
		resp, err := e.router.Complete(ctx, background("x"))
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, resp.Provider)
	}
	// The rest go to b, which has no cap of its own.
	if want := "a a b b"; strings.Join(got, " ") != want {
		t.Fatalf("providers = %v, want %s", got, want)
	}
}

func TestBackgroundGlobalReserve(t *testing.T) {
	a := newFake(t, ok("a", 1, 1))
	e := setup(t, provider("a", a))
	ctx := t.Context()
	if err := settings.SetValue(ctx, e.store, llm.SettingGlobalDailyRequestCap, 10, nil); err != nil {
		t.Fatal(err)
	}
	for range 6 {
		if _, err := e.router.Complete(ctx, ask("i")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.router.Complete(ctx, background("b")); !errors.Is(err, llm.ErrDeferred) {
		t.Fatalf("err = %v, want ErrDeferred at the global reserve", err)
	}
	if ok, _ := e.router.Headroom(ctx, llm.Needs{}); ok {
		t.Fatal("Headroom ok under the global reserve")
	}
}

func TestBackgroundCacheHitsCostNothingAndNothingConfiguredIsUnavailable(t *testing.T) {
	a := newFake(t, ok("a", 1, 1))
	pa := provider("a", a)
	pa.DailyRequestCap = 10
	e := setup(t, pa)
	ctx := t.Context()
	req := background("same")
	req.Feature.CacheTTL = 3600e9
	if _, err := e.router.Complete(ctx, req); err != nil {
		t.Fatal(err)
	}
	for range 6 { // burn the quota down to the reserve
		if _, err := e.router.Complete(ctx, ask("i")); err != nil {
			t.Fatal(err)
		}
	}
	if resp, err := e.router.Complete(ctx, req); err != nil || !resp.Cached {
		t.Fatalf("cached background request: %+v %v", resp, err)
	}

	empty := setup(t)
	if _, err := empty.router.Complete(ctx, background("x")); !errors.Is(err, llm.ErrUnavailable) || errors.Is(err, llm.ErrDeferred) {
		t.Fatalf("no providers: %v", err)
	}
	if ok, _ := empty.router.Headroom(ctx, llm.Needs{}); ok {
		t.Fatal("Headroom ok without providers")
	}
}
