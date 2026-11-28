package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Reel limits.
const (
	maxReelSeconds  = 10 * 60
	infoTimeout     = 45 * time.Second
	audioTimeout    = 3 * time.Minute
	toolTimeout     = 10 * time.Second
	maxCaptionRunes = 6000
)

// ErrReelBlocked means yt-dlp could not read the post without a login (or
// was rate limited); the user pastes the caption instead.
var ErrReelBlocked = errors.New("instagram did not let us read this post")

// ErrToolMissing means yt-dlp (or ffmpeg) is not installed on the server.
var ErrToolMissing = errors.New("yt-dlp is not installed on the server")

// Reel is what a public reel gives without logging in.
type Reel struct {
	Caption  string
	Title    string
	Uploader string
	Seconds  float64
	// Audio is the extracted track (m4a), nil when the post has none or it
	// is over ten minutes.
	Audio []byte
}

// Reels reads Instagram posts.
type Reels interface {
	Read(ctx context.Context, postURL string, withAudio bool) (Reel, error)
	Status(ctx context.Context) ToolStatus
}

// ToolStatus is what Admin → Recipes shows about the tools.
type ToolStatus struct {
	YtDLP, FFmpeg               bool
	YtDLPVersion, FFmpegVersion string
	Error                       string
}

// instagramHost matches the hosts an Instagram import may name.
var instagramHost = regexp.MustCompile(`(?i)^(?:www\.|m\.)?(?:instagram\.com|instagr\.am)$`)

// IsInstagram reports whether raw is an Instagram post or reel URL.
func IsInstagram(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return (u.Scheme == "https" || u.Scheme == "http") && instagramHost.MatchString(u.Hostname())
}

var reelPath = regexp.MustCompile(`^/(?:[A-Za-z0-9_.]+/)?(?:reel|reels|p|tv)/([A-Za-z0-9_-]{5,64})/?$`)

// canonicalReel rebuilds a post URL from its shortcode, so nothing but a
// plain https Instagram URL ever reaches yt-dlp.
func canonicalReel(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !instagramHost.MatchString(u.Hostname()) {
		return "", ErrBadURL
	}
	m := reelPath.FindStringSubmatch(u.Path)
	if m == nil {
		return "", fmt.Errorf("%w: not an Instagram post or reel link", ErrBadURL)
	}
	return "https://www.instagram.com/reel/" + m[1] + "/", nil
}

// YtDLP runs yt-dlp and ffmpeg from PATH. No cookies or login are ever
// passed (AJ, 2026-11-27): private or login-walled posts go to needs_input.
type YtDLP struct {
	// Bin and FFmpeg default to "yt-dlp" and "ffmpeg" on PATH.
	Bin, FFmpeg string
	// TempDir defaults to os.TempDir().
	TempDir string

	mu     sync.Mutex
	cached ToolStatus
	at     time.Time
}

// Status reports whether the tools are installed and their versions
// (cached for a minute).
func (y *YtDLP) Status(ctx context.Context) ToolStatus {
	y.mu.Lock()
	defer y.mu.Unlock()
	if !y.at.IsZero() && time.Since(y.at) < time.Minute {
		return y.cached
	}
	var st ToolStatus
	if out, err := y.run(ctx, toolTimeout, y.bin(), "--version"); err == nil {
		st.YtDLP, st.YtDLPVersion = true, firstLine(out)
	} else {
		st.Error = err.Error()
	}
	if out, err := y.run(ctx, toolTimeout, y.ffmpeg(), "-hide_banner", "-version"); err == nil {
		st.FFmpeg = true
		st.FFmpegVersion = strings.TrimPrefix(strings.Fields(firstLine(out) + " x x")[2], "n")
	}
	y.cached, y.at = st, time.Now()
	return st
}

func (y *YtDLP) bin() string {
	if y.Bin != "" {
		return y.Bin
	}
	return "yt-dlp"
}

func (y *YtDLP) ffmpeg() string {
	if y.FFmpeg != "" {
		return y.FFmpeg
	}
	return "ffmpeg"
}

func firstLine(b []byte) string {
	s, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return strings.TrimSpace(s)
}

// common are flags for every yt-dlp call: no config files, no cache, no
// cookies, quiet, bounded network.
var common = []string{
	"--ignore-config", "--no-cache-dir", "--no-cookies", "--no-cookies-from-browser", "--no-playlist", "--no-warnings",
	"--socket-timeout", "15", "--retries", "2", "--no-progress",
}

// Read gets the caption and, if asked and short enough, the audio track.
func (y *YtDLP) Read(ctx context.Context, postURL string, withAudio bool) (Reel, error) {
	link, err := canonicalReel(postURL)
	if err != nil {
		return Reel{}, err
	}
	if _, err := exec.LookPath(y.bin()); err != nil {
		return Reel{}, ErrToolMissing
	}
	args := append(append([]string{}, common...), "--skip-download", "--dump-single-json", "--", link)
	out, err := y.run(ctx, infoTimeout, y.bin(), args...)
	if err != nil {
		return Reel{}, classifyYtDLP(err)
	}
	var info struct {
		Description string  `json:"description"`
		Title       string  `json:"title"`
		Uploader    string  `json:"uploader"`
		Duration    float64 `json:"duration"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return Reel{}, fmt.Errorf("yt-dlp: unreadable output: %w", err)
	}
	r := Reel{
		Caption: capText(strings.TrimSpace(info.Description), maxCaptionRunes), Title: strings.TrimSpace(info.Title),
		Uploader: info.Uploader, Seconds: info.Duration,
	}
	if !withAudio || info.Duration <= 0 || info.Duration > maxReelSeconds {
		return r, nil
	}
	if _, err := exec.LookPath(y.ffmpeg()); err != nil {
		return r, nil // caption only: the transcript is a bonus
	}
	dir, err := os.MkdirTemp(y.TempDir, "stockpot-reel-*")
	if err != nil {
		return r, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	args = append(append([]string{}, common...),
		"-f", "bestaudio/best", "-x", "--audio-format", "m4a", "--audio-quality", "5",
		"--match-filter", fmt.Sprintf("duration <= %d", maxReelSeconds), "--max-filesize", "40M",
		"--ffmpeg-location", y.ffmpeg(), "-o", filepath.Join(dir, "audio.%(ext)s"), "--", link)
	if _, err := y.run(ctx, audioTimeout, y.bin(), args...); err != nil {
		if ctx.Err() != nil {
			return r, ctx.Err()
		}
		return r, nil // keep the caption; audio is optional
	}
	data, err := os.ReadFile(filepath.Join(dir, "audio.m4a")) //nolint:gosec // our temp dir
	if err == nil && len(data) > 0 && len(data) <= 25<<20 {
		r.Audio = data
	}
	return r, nil
}

type runError struct {
	err    error
	stderr string
}

func (e *runError) Error() string {
	s := e.err.Error()
	if e.stderr != "" {
		s += ": " + e.stderr
	}
	return s
}

func (e *runError) Unwrap() error { return e.err }

func (y *YtDLP) run(ctx context.Context, timeout time.Duration, bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // fixed tools; the URL is rebuilt from a shortcode and follows "--"
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "LANG=C.UTF-8"}
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &limited{w: &stderr, n: 4096}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, &runError{err: fmt.Errorf("timed out after %s: %w", timeout, ctx.Err())}
		}
		return nil, &runError{err: err, stderr: strings.TrimSpace(stderr.String())}
	}
	return stdout.Bytes(), nil
}

// blockedHints are yt-dlp messages that mean "needs a login or a human".
var blockedHints = regexp.MustCompile(`(?i)login required|log in|cookies|rate.?limit|private|not available|empty media response|unable to extract|requested content is not available|401|403|429`)

func classifyYtDLP(err error) error {
	var re *runError
	if errors.As(err, &re) && blockedHints.MatchString(re.stderr) {
		return fmt.Errorf("%w (%s)", ErrReelBlocked, truncate(lastLine(re.stderr), 200))
	}
	if errors.Is(err, exec.ErrNotFound) {
		return ErrToolMissing
	}
	return fmt.Errorf("yt-dlp failed: %w", err)
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}

// limited keeps the first n bytes written.
type limited struct {
	w interface{ Write([]byte) (int, error) }
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}
