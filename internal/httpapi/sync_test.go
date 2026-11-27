package httpapi

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/identity"
)

func cmdBody(typ string, payload map[string]any) map[string]any {
	return map[string]any{"id": uuid.NewString(), "type": typ, "clientTime": time.Now().UTC().Format(time.RFC3339), "payload": payload}
}

func TestCommandsAndSyncOverHTTP(t *testing.T) {
	e := newEnv(t)
	admin, _ := e.setupAdmin()

	// Public callers are turned away.
	e.client().fail(http.MethodGet, "/v1/sync", nil, http.StatusUnauthorized, "unauthenticated")

	pid := uuid.NewString()
	create := cmdBody("catalog.product.create", map[string]any{"id": pid, "name": "Mandarins", "brand": nil, "defaultQty": "2", "defaultUnit": "lb"})
	var res CommandResults
	admin.ok(http.MethodPost, "/v1/commands", map[string]any{"commands": []any{create}}, &res)
	if len(res.Results) != 1 || res.Results[0].Status != CommandResultStatusApplied {
		t.Fatalf("results %+v", res)
	}
	admin.ok(http.MethodPost, "/v1/commands", map[string]any{"commands": []any{create}}, &res)
	if res.Results[0].Status != CommandResultStatusDuplicate {
		t.Fatalf("replay %+v", res)
	}
	bad := cmdBody("catalog.product.update", map[string]any{"id": uuid.NewString(), "note": "x"})
	admin.ok(http.MethodPost, "/v1/commands", map[string]any{"commands": []any{bad}}, &res)
	if res.Results[0].Status != CommandResultStatusRejected || res.Results[0].Problem.Code != "gone" {
		t.Fatalf("missing product %+v", res)
	}

	// The schema, not the handler, refuses unknown commands and fields.
	admin.fail(http.MethodPost, "/v1/commands", map[string]any{"commands": []any{cmdBody("catalog.nope", map[string]any{"id": pid})}}, http.StatusBadRequest, "bad_request")
	admin.fail(http.MethodPost, "/v1/commands", map[string]any{"commands": []any{cmdBody("catalog.store.create", map[string]any{"id": pid, "name": "S", "x": 1})}}, http.StatusBadRequest, "bad_request")
	admin.fail(http.MethodPost, "/v1/commands", map[string]any{"commands": []any{}}, http.StatusBadRequest, "bad_request")

	var page struct {
		Rows       map[string][]map[string]any `json:"rows"`
		Tombstones map[string][]string         `json:"tombstones"`
		Next       int64                       `json:"next"`
		NextGlobal int64                       `json:"nextGlobal"`
		More       bool                        `json:"more"`
	}
	admin.ok(http.MethodGet, "/v1/sync", nil, &page)
	if len(page.Rows["product"]) != 1 || len(page.Rows["location"]) != 3 || page.Next == 0 || page.More {
		t.Fatalf("sync page: products=%d locations=%d next=%d more=%v", len(page.Rows["product"]), len(page.Rows["location"]), page.Next, page.More)
	}
	if page.Rows["product"][0]["default_qty"] != "2" {
		t.Fatalf("product row %v", page.Rows["product"][0])
	}
	var again struct {
		Rows map[string][]map[string]any `json:"rows"`
		Next int64                       `json:"next"`
	}
	admin.ok(http.MethodGet, "/v1/sync?since="+itoa(page.Next)+"&sinceGlobal="+itoa(page.NextGlobal), nil, &again)
	if len(again.Rows) != 0 || again.Next != page.Next {
		t.Fatalf("nothing new expected: %+v", again)
	}
	if e.auditCount("catalog.product.create") != 1 {
		t.Fatal("command not audited once")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestSyncEventsStream(t *testing.T) {
	e := newEnv(t)
	admin, _ := e.setupAdmin()
	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/v1/sync/events", nil)
	req.Header.Set("Origin", identityOrigin)
	req.AddCookie(&http.Cookie{Name: identity.SessionCookie, Value: admin.cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("status %d headers %v", resp.StatusCode, resp.Header)
	}
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	wait := func(want string) {
		t.Helper()
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("stream ended before %q", want)
				}
				if l == want {
					return
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("no %q within 5s", want)
			}
		}
	}
	wait(": connected")

	body := map[string]any{"commands": []any{cmdBody("catalog.store.create", map[string]any{"id": uuid.NewString(), "name": "Costco"})}}
	admin.ok(http.MethodPost, "/v1/commands", body, nil)
	wait("event: changed")
}
