package importer

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// loopbackOK lets tests reach httptest servers (127.0.0.1) while every other
// rule of PublicOnly still applies.
func loopbackOK(a netip.Addr) error {
	if a.Unmap() == netip.MustParseAddr("127.0.0.1") {
		return nil
	}
	return PublicOnly(nil)(a)
}

func TestPublicOnly(t *testing.T) {
	own := netip.MustParseAddr("203.0.114.7")
	p := PublicOnly([]netip.Addr{own})
	for _, s := range []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.100.1.1",
		"0.0.0.0", "::1", "fe80::1", "fd00::1", "::ffff:10.0.0.1", "64:ff9b::a00:1", "224.0.0.1", "203.0.114.7",
	} {
		if err := p(netip.MustParseAddr(s)); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%s allowed", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "151.101.1.1", "2606:4700::1111"} {
		if err := p(netip.MustParseAddr(s)); err != nil {
			t.Errorf("%s refused: %v", s, err)
		}
	}
}

func page(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}
}

func TestFetchPage(t *testing.T) {
	srv := httptest.NewServer(page("<html>hi</html>"))
	defer srv.Close()
	p, lg, err := NewFetcher(loopbackOK).FetchPage(t.Context(), srv.URL+"/r#frag")
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Body) != "<html>hi</html>" || lg.Status != 200 || lg.Bytes != 15 || lg.Purpose != "page" {
		t.Fatalf("page %q, log %+v", p.Body, lg)
	}
}

func TestFetchRefusesLoopbackByDefault(t *testing.T) {
	srv := httptest.NewServer(page("secret"))
	defer srv.Close()
	_, _, err := NewFetcher(PublicOnly(nil)).FetchPage(t.Context(), srv.URL)
	if !errors.Is(err, ErrBlockedAddress) || Retryable(err) {
		t.Fatalf("err = %v", err)
	}
	// A hostname that resolves to loopback is refused the same way: the check
	// runs on the resolved address at connect time.
	_, _, err = NewFetcher(PublicOnly(nil)).FetchPage(t.Context(), strings.Replace(srv.URL, "127.0.0.1", "localhost", 1))
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("localhost: %v", err)
	}
}

func TestFetchRefusesRedirectToPrivateAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/meta":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		case "/lan":
			http.Redirect(w, r, "http://10.0.0.1:8080/admin", http.StatusMovedPermanently)
		case "/file":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		}
	}))
	defer srv.Close()
	f := NewFetcher(loopbackOK)
	for _, path := range []string{"/meta", "/lan"} {
		_, lg, err := f.FetchPage(t.Context(), srv.URL+path)
		if !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%s: err = %v", path, err)
		}
		if lg.Err == nil {
			t.Errorf("%s: log has no error", path)
		}
	}
	if _, _, err := f.FetchPage(t.Context(), srv.URL+"/file"); !errors.Is(err, ErrBadURL) {
		t.Errorf("file redirect: %v", err)
	}
}

func TestFetchLimits(t *testing.T) {
	var hops int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/loop":
			hops++
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/big":
			w.Header().Set("Content-Type", "text/html")
			chunk := make([]byte, 1<<20)
			for range 6 {
				_, _ = w.Write(chunk)
			}
		case "/pdf":
			w.Header().Set("Content-Type", "application/pdf")
		case "/gone":
			w.WriteHeader(http.StatusNotFound)
		case "/busy":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/img":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte{0xff, 0xd8, 0xff})
		}
	}))
	defer srv.Close()
	f := NewFetcher(loopbackOK)
	if _, _, err := f.FetchPage(t.Context(), srv.URL+"/loop"); !errors.Is(err, ErrTooManyHops) || hops != maxRedirects+1 {
		t.Errorf("loop: %v after %d hops", err, hops)
	}
	if _, _, err := f.FetchPage(t.Context(), srv.URL+"/big"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("big: %v", err)
	}
	if _, _, err := f.FetchPage(t.Context(), srv.URL+"/pdf"); !errors.Is(err, ErrNotHTML) {
		t.Errorf("pdf: %v", err)
	}
	if _, _, err := f.FetchPage(t.Context(), srv.URL+"/gone"); Retryable(err) {
		t.Errorf("404 retryable: %v", err)
	}
	if _, _, err := f.FetchPage(t.Context(), srv.URL+"/busy"); !Retryable(err) {
		t.Errorf("503 not retryable: %v", err)
	}
	if _, _, err := f.FetchImage(t.Context(), srv.URL+"/pdf"); !errors.Is(err, ErrNotImage) {
		t.Errorf("image pdf: %v", err)
	}
	if p, _, err := f.FetchImage(t.Context(), srv.URL+"/img"); err != nil || len(p.Body) != 3 {
		t.Errorf("image: %v", err)
	}
	for _, bad := range []string{"ftp://x.test/", "javascript:alert(1)", "http://user:pw@x.test/", "/relative"} {
		if _, _, err := f.FetchPage(t.Context(), bad); !errors.Is(err, ErrBadURL) {
			t.Errorf("%s: %v", bad, err)
		}
	}
}
