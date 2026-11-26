package audit

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/reqctx"
	"github.com/a-jay98/stockpot/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func TestRecordTakesWhoFromContext(t *testing.T) {
	pool := testdb.New(t)
	user, hh, sess := uuid.New(), uuid.New(), uuid.New()
	ctx := reqctx.With(t.Context(), &reqctx.Info{RequestID: "req-1", ClientIP: "192.0.2.7", UserID: user, HouseholdID: hh, SessionID: sess})
	if err := Record(ctx, pool, "user.update", UserTarget(user), map[string]string{"name": "a"}, map[string]string{"name": "b"}); err != nil {
		t.Fatal(err)
	}
	var actor, household, session uuid.UUID
	var reqID, ip, before, after string
	if err := pool.QueryRow(t.Context(), `
		SELECT actor_id, household_id, session_id, request_id, host(ip), before::text, after::text
		FROM audit_event WHERE action = 'user.update'`).Scan(&actor, &household, &session, &reqID, &ip, &before, &after); err != nil {
		t.Fatal(err)
	}
	if actor != user || household != hh || session != sess || reqID != "req-1" || ip != "192.0.2.7" ||
		before != `{"name": "a"}` || after != `{"name": "b"}` {
		t.Fatalf("got %v %v %v %s %s %s %s", actor, household, session, reqID, ip, before, after)
	}

	// WithActor fills in for requests without a session (sign-in).
	other := uuid.New()
	if err := Record(WithActor(context.Background(), other, uuid.Nil), pool, "auth.login", UserTarget(other), nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), "SELECT actor_id FROM audit_event WHERE action = 'auth.login'").Scan(&actor); err != nil || actor != other {
		t.Fatalf("actor %v err %v", actor, err)
	}

	if err := Record(ctx, pool, "Bad Action", Target{}, nil, nil); err == nil {
		t.Fatal("invalid action accepted")
	}
}

func TestAccessWriterBatches(t *testing.T) {
	pool := testdb.New(t)
	w := NewAccessWriter(pool, 16)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	user := uuid.New()
	for i := range 20 { // more than the queue holds: some are dropped, none block
		w.Log(Access{
			Time: time.Now(), Duration: time.Duration(i) * time.Millisecond, Method: "GET", Route: "/api/v1/me", Path: "/api/v1/me",
			Status: 200, Request: reqctx.Info{RequestID: "r", UserID: user, ClientIP: "::1"},
		})
	}
	cancel()
	<-done
	var n int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM access_log WHERE user_id = $1", user).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if uint64(n) != w.Written() || w.Written()+w.Dropped() != 20 || n == 0 {
		t.Fatalf("rows %d written %d dropped %d", n, w.Written(), w.Dropped())
	}
}
