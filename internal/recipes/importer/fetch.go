package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Fetch limits (architecture-m2 §7).
const (
	fetchTimeout    = 10 * time.Second
	maxPageBytes    = 5 << 20
	maxImageBytes   = 8 << 20
	maxRedirects    = 5
	fetchUserAgent  = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36 Stockpot/1"
	fetchAcceptHTML = "text/html,application/xhtml+xml;q=0.9,*/*;q=0.1"
)

// Fetch errors. Every one but ErrFetchNetwork and a 5xx/429 StatusError is
// permanent: retrying the same URL can't help.
var (
	// ErrBlockedAddress: the host resolves to an address the server must not
	// reach (private, loopback, link-local, the server's own).
	ErrBlockedAddress = errors.New("address not allowed")
	ErrBadURL         = errors.New("not an http(s) URL")
	ErrTooLarge       = errors.New("response too large")
	ErrNotHTML        = errors.New("not a web page")
	ErrNotImage       = errors.New("not an image")
	ErrTooManyHops    = errors.New("too many redirects")
	// ErrFetchNetwork: DNS, connect, TLS or timeout failure.
	ErrFetchNetwork = errors.New("could not reach the site")
)

// StatusError is a non-2xx answer.
type StatusError struct{ Status int }

func (e *StatusError) Error() string { return fmt.Sprintf("the site answered HTTP %d", e.Status) }

// Retryable reports whether a fetch error may go away on a later attempt.
func Retryable(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Status >= 500 || se.Status == http.StatusTooManyRequests || se.Status == http.StatusRequestTimeout
	}
	return errors.Is(err, ErrFetchNetwork)
}

// AddrPolicy decides whether the fetcher may connect to an address.
type AddrPolicy func(netip.Addr) error

// extraBlocked are ranges the netip predicates don't cover.
var extraBlocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT (Tailscale too)
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, broadcast
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64 (embeds IPv4)
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local NAT64
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("2002::/16"),       // 6to4 (embeds IPv4)
	netip.MustParsePrefix("fec0::/10"),       // site-local (deprecated)
}

// PublicOnly refuses private, loopback, link-local, multicast, unspecified
// and reserved addresses, plus own (the server's interface addresses, so a
// page can't make it call itself on its public IP).
func PublicOnly(own []netip.Addr) AddrPolicy {
	mine := map[netip.Addr]bool{}
	for _, a := range own {
		mine[a.Unmap()] = true
	}
	return func(a netip.Addr) error {
		a = a.Unmap()
		switch {
		case !a.IsValid(), a.IsUnspecified(), a.IsLoopback(), a.IsPrivate(), a.IsLinkLocalUnicast(),
			a.IsLinkLocalMulticast(), a.IsInterfaceLocalMulticast(), a.IsMulticast(), mine[a]:
			return fmt.Errorf("%w: %s", ErrBlockedAddress, a)
		}
		for _, p := range extraBlocked {
			if p.Contains(a) {
				return fmt.Errorf("%w: %s", ErrBlockedAddress, a)
			}
		}
		return nil
	}
}

// OwnAddresses lists the server's interface addresses.
func OwnAddresses() []netip.Addr {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil {
			out = append(out, p.Addr())
		}
	}
	return out
}

// FetchLog records one fetch (table recipe_fetch).
type FetchLog struct {
	Purpose   string // page or image
	URL       string
	FinalURL  string
	Status    int
	Bytes     int64
	Duration  time.Duration
	Redirects int
	Err       error
}

// Fetcher is the hardened HTTP client for user-supplied URLs. The address
// policy is checked when each connection is made (after DNS resolution, on
// every redirect hop and every retry), so DNS rebinding can't slip a private
// address past it. It never uses an HTTP proxy.
type Fetcher struct {
	client *http.Client
	policy AddrPolicy
}

// NewFetcher returns a fetcher that only connects where policy allows.
func NewFetcher(policy AddrPolicy) *Fetcher {
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: %s", ErrBlockedAddress, address)
			}
			return policy(ap.Addr())
		},
	}
	t := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: fetchTimeout,
	}
	f := &Fetcher{policy: policy}
	f.client = &http.Client{
		Transport: t,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return ErrTooManyHops
			}
			return checkURL(req.URL)
		},
	}
	return f
}

func checkURL(u *url.URL) error {
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ErrBadURL
	}
	return nil
}

// ParseURL validates a user-supplied URL.
func ParseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, ErrBadURL
	}
	if err := checkURL(u); err != nil {
		return nil, err
	}
	u.Fragment = ""
	return u, nil
}

// Page is a fetched document.
type Page struct {
	URL         *url.URL // after redirects
	Body        []byte
	ContentType string
}

// FetchPage gets an HTML page.
func (f *Fetcher) FetchPage(ctx context.Context, raw string) (Page, FetchLog, error) {
	return f.fetch(ctx, raw, "page", fetchAcceptHTML, maxPageBytes, func(mt string) error {
		if mt != "text/html" && mt != "application/xhtml+xml" {
			return ErrNotHTML
		}
		return nil
	})
}

// FetchImage gets a cover image (JPEG, PNG or WebP, what the photo pipeline takes).
func (f *Fetcher) FetchImage(ctx context.Context, raw string) (Page, FetchLog, error) {
	return f.fetch(ctx, raw, "image", "image/webp,image/jpeg,image/png;q=0.9", maxImageBytes, func(mt string) error {
		switch mt {
		case "image/jpeg", "image/png", "image/webp":
			return nil
		}
		return ErrNotImage
	})
}

func (f *Fetcher) fetch(ctx context.Context, raw, purpose, accept string, limit int64, okType func(string) error) (Page, FetchLog, error) {
	lg := FetchLog{Purpose: purpose, URL: raw}
	start := time.Now()
	page, err := f.do(ctx, raw, accept, limit, okType, &lg)
	lg.Duration, lg.Err = time.Since(start), err
	return page, lg, err
}

func (f *Fetcher) do(ctx context.Context, raw, accept string, limit int64, okType func(string) error, lg *FetchLog) (Page, error) {
	u, err := ParseURL(raw)
	if err != nil {
		return Page{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Page{}, ErrBadURL
	}
	req.Header.Set("User-Agent", fetchUserAgent)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "en-CA,en;q=0.9,*;q=0.5")
	resp, err := f.client.Do(req)
	if err != nil {
		return Page{}, classifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()
	lg.Status = resp.StatusCode
	lg.FinalURL = resp.Request.URL.String()
	for r := resp.Request; r.Response != nil; r = r.Response.Request {
		lg.Redirects++
	}
	if resp.StatusCode/100 != 2 {
		return Page{}, &StatusError{Status: resp.StatusCode}
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err := okType(strings.ToLower(mt)); err != nil {
		return Page{}, err
	}
	if resp.ContentLength > limit {
		return Page{}, ErrTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	lg.Bytes = int64(len(body))
	if err != nil {
		return Page{}, classifyTransport(err)
	}
	if int64(len(body)) > limit {
		return Page{}, ErrTooLarge
	}
	return Page{URL: resp.Request.URL, Body: body, ContentType: mt}, nil
}

// classifyTransport keeps policy and redirect errors recognizable and folds
// the rest into ErrFetchNetwork.
func classifyTransport(err error) error {
	for _, e := range []error{ErrBlockedAddress, ErrTooManyHops, ErrBadURL} {
		if errors.Is(err, e) {
			return fmt.Errorf("%w", unwrapTo(err, e))
		}
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrFetchNetwork, shortNetErr(err)) //nolint:errorlint // flatten: the cause is logged, not branched on
}

// unwrapTo returns the innermost error in err's chain that wraps target (so
// "address not allowed: 10.0.0.1" survives without url.Error's prefix).
func unwrapTo(err, target error) error {
	last := target
	for e := err; e != nil; e = errors.Unwrap(e) {
		if errors.Is(e, target) {
			last = e
		}
	}
	return last
}

func shortNetErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timed out"
	}
	s := err.Error()
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
