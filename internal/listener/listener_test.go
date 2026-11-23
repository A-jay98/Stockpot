package listener

import "testing"

func TestListenFallsBackToTCP(t *testing.T) {
	t.Setenv("LISTEN_PID", "")
	t.Setenv("LISTEN_FDS", "")
	ln, err := Listen(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if ln.Addr().Network() != "tcp" {
		t.Fatalf("network = %q, want tcp", ln.Addr().Network())
	}
}
