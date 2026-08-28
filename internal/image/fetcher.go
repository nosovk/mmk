package image

import (
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nosovk/mmk/internal/debuglog"

	"golang.org/x/image/draw"
	// Register WebP with the stdlib image registry so provider-hosted
	// avatars and attachments decode after a successful download.
	_ "golang.org/x/image/webp"
	"golang.org/x/sync/singleflight"
)

// FetchRequest describes one image fetch.
type FetchRequest struct {
	Key        string      // stable cache key supplied by the provider
	URL        string      // remote URL
	Target     image.Point // target downscale size in pixels (0 = no downscale)
	CellTarget image.Point // optional target in terminal cells; when nonzero,
	// the fetcher will pre-render the image into the
	// active prerender protocol for this cell footprint.
	// ReqID is a debuglog correlator threaded by the caller from
	// debuglog.NextReqID() at enqueue time. Zero means "not threaded"
	// (e.g. avatar fetches that don't participate in the per-image
	// timeline log). Logged as req_id=N in the [imgfetch] surface.
	ReqID uint64
}

// FetchResult is the decoded, downscaled image plus on-disk metadata.
type FetchResult struct {
	Img    image.Image
	Source string // path on disk
	Mime   string
}

// Fetcher downloads images, stores raw bytes in Cache, decodes, and
// downscales. Concurrent fetches for the same Key are deduplicated;
// across-key concurrency is bounded by a semaphore so a channel full
// of images doesn't overwhelm an upstream server.
type Fetcher struct {
	cache *Cache
	http  *http.Client
	sf    singleflight.Group
	sem   chan struct{} // bounded concurrency for cross-key fetches

	// decoded caches the result of Cached() per (key, target) so
	// repeated calls don't re-decode the on-disk PNG and re-bilinear
	// downscale on every cache rebuild. Without this, a burst of
	// ImageReadyMsg events (e.g., older-history load completing for
	// 25 attachments) triggers N cache rebuilds × N decodes each
	// = N² synchronous decode work on the UI thread, making the app
	// appear hung for many seconds.
	decoded sync.Map // string("<key>|<wxh>") -> image.Image

	// prerendered caches the result of RenderImage(proto, decoded, cellTarget)
	// per (key, cellTarget, proto). The fetch goroutine populates this so
	// the bubbletea Update goroutine never runs sixel encoding, halfblock
	// per-pixel ANSI building, or kitty PNG-encode-and-base64 work.
	//
	// Entries are retained for the lifetime of the Fetcher. For sixel this
	// holds the full encoded byte stream (~50-300 KB per image) inside the
	// Render's OnFlush closure; combined with the Task 1 decoded sync.Map
	// every fetched attachment has two permanent memory residents per
	// session. TODO(perf): bound with an LRU keyed on access recency once
	// the messages pane evicts off-screen entries.
	//
	// The pointer is swappable under prerenderMu so ConfigurePrerender can
	// drop the cache atomically without racing in-flight maybePrerender
	// stores: a stale worker writes to the old (now-orphaned) map and the
	// store is harmlessly GC'd.
	prerendered    *sync.Map // string("<key>|<cw>x<ch>|<proto>") -> Render
	prerenderMu    sync.RWMutex
	prerenderProto Protocol
	prerenderKitty *KittyRenderer // non-nil when prerenderProto == ProtoKitty

}

// fetchConcurrencyLimit caps the number of in-flight HTTP fetches.
// Four keeps the messages pane responsive without overwhelming image hosts.
const fetchConcurrencyLimit = 4

// rateLimitMaxRetries / rateLimitInitialBackoff / rateLimitMaxBackoff
// govern automatic retry on HTTP 429. Cap at three retries so a stuck
// upstream does not block forever.
const (
	rateLimitMaxRetries     = 3
	rateLimitInitialBackoff = 500 * time.Millisecond
	rateLimitMaxBackoff     = 4 * time.Second
)

// NewFetcher constructs a Fetcher. If client is nil, a default with a
// 10-second timeout is used. Providers that need authenticated image
// downloads must supply a client whose transport owns those credentials.
func NewFetcher(cache *Cache, client *http.Client) *Fetcher {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Fetcher{
		cache:       cache,
		http:        client,
		sem:         make(chan struct{}, fetchConcurrencyLimit),
		prerendered: &sync.Map{},
	}
}

// ConfigurePrerender enables eager protocol encoding in the fetch
// goroutine. After every successful Fetch whose request carries a
// non-zero CellTarget, the decoded image is run through
// RenderImage(proto, ..., cellTarget) and stashed for retrieval via
// Prerendered. Pass ProtoOff to disable.
//
// Safe to call at startup or whenever the active protocol changes
// (theme switch / terminal capability re-probe). Resets the prerender
// cache.
func (f *Fetcher) ConfigurePrerender(proto Protocol) {
	f.prerenderMu.Lock()
	defer f.prerenderMu.Unlock()
	f.prerenderProto = proto
	f.prerendered = &sync.Map{}
}

// ConfigurePrerenderKitty hooks a KittyRenderer into the prerender
// pipeline. Must be called when proto == ProtoKitty so the worker can
// SetSource + RenderKey on the same renderer the UI thread will look up
// from. Pass nil to clear.
func (f *Fetcher) ConfigurePrerenderKitty(kr *KittyRenderer) {
	f.prerenderMu.Lock()
	defer f.prerenderMu.Unlock()
	f.prerenderKitty = kr
}

// Prerendered returns a previously-prepared Render for (key, cellTarget,
// proto), or (zero, false) if none. Safe to call from the UI thread; pure
// map lookup, no decode, no encode.
func (f *Fetcher) Prerendered(key string, cellTarget image.Point, proto Protocol) (Render, bool) {
	f.prerenderMu.RLock()
	m := f.prerendered
	f.prerenderMu.RUnlock()
	mk := prerenderKey(key, cellTarget, proto)
	if v, ok := m.Load(mk); ok {
		if r, ok := v.(Render); ok {
			return r, true
		}
	}
	return Render{}, false
}

func prerenderKey(key string, cellTarget image.Point, proto Protocol) string {
	return fmt.Sprintf("%s|%dx%d|%d", key, cellTarget.X, cellTarget.Y, proto)
}

// Fetch returns the decoded image, downloading and caching if needed.
// Cross-key concurrency is bounded; if the limit is full, additional
// callers wait until a slot frees up or ctx is canceled.
func (f *Fetcher) Fetch(ctx context.Context, req FetchRequest) (FetchResult, error) {
	leader := false
	v, err, shared := f.sf.Do(req.Key, func() (any, error) {
		leader = true
		debuglog.ImgFetch("sf-leader: key=%s req_id=%d", req.Key, req.ReqID)
		// Cache-only path doesn't need an HTTP slot; check first.
		if _, hit := f.cache.Get(req.Key); !hit {
			semStart := time.Now()
			select {
			case f.sem <- struct{}{}:
				debuglog.ImgFetch("sem-acquire: key=%s req_id=%d wait_ms=%d",
					req.Key, req.ReqID, time.Since(semStart).Milliseconds())
				defer func() { <-f.sem }()
			case <-ctx.Done():
				debuglog.ImgFetch("sem-cancel: key=%s req_id=%d wait_ms=%d err=%v",
					req.Key, req.ReqID, time.Since(semStart).Milliseconds(), ctx.Err())
				return FetchResult{}, ctx.Err()
			}
		} else {
			debuglog.ImgFetch("sem-skip: key=%s req_id=%d (disk cache hit, no HTTP needed)",
				req.Key, req.ReqID)
		}
		return f.fetchInner(ctx, req)
	})
	if !leader {
		debuglog.ImgFetch("sf-join: key=%s req_id=%d shared=%v leader=false",
			req.Key, req.ReqID, shared)
	}
	if err != nil {
		return FetchResult{}, err
	}
	return v.(FetchResult), nil
}

func (f *Fetcher) fetchInner(ctx context.Context, req FetchRequest) (FetchResult, error) {
	path, hit := f.cache.Get(req.Key)
	diskResult := "miss"
	if hit {
		diskResult = "hit"
	}
	debuglog.ImgFetch("disk-cache: key=%s req_id=%d result=%s", req.Key, req.ReqID, diskResult)
	if !hit {
		dlStart := time.Now()
		body, ct, err := f.download(ctx, req.URL)
		if err != nil {
			debuglog.ImgFetch("download-err: key=%s req_id=%d dur_ms=%d err=%v",
				req.Key, req.ReqID, time.Since(dlStart).Milliseconds(), err)
			return FetchResult{}, err
		}
		debuglog.ImgFetch("download-ok: key=%s req_id=%d dur_ms=%d bytes=%d ct=%s",
			req.Key, req.ReqID, time.Since(dlStart).Milliseconds(), len(body), ct)
		ext := extFromMime(ct, req.URL)
		path, err = f.cache.Put(req.Key, ext, body)
		if err != nil {
			debuglog.ImgFetch("cache-put-err: key=%s req_id=%d err=%v", req.Key, req.ReqID, err)
			return FetchResult{}, err
		}
	}

	file, err := os.Open(path)
	if err != nil {
		debuglog.ImgFetch("open-err: key=%s req_id=%d path=%s err=%v",
			req.Key, req.ReqID, path, err)
		return FetchResult{}, err
	}
	defer file.Close()

	decStart := time.Now()
	img, _, err := image.Decode(file)
	if err != nil {
		// Cache poisoning recovery: a previously persisted file isn't
		// decodable as an image (e.g., an HTML auth-failure response from
		// before this auth path was wired up). Evict so the next Fetch
		// re-downloads with the now-correct credentials.
		file.Close()
		f.cache.Delete(req.Key)
		debuglog.ImgFetch("decode-err: key=%s req_id=%d path=%s err=%v (cache evicted)",
			req.Key, req.ReqID, path, err)
		return FetchResult{}, fmt.Errorf("decode %s: %w (cache evicted)", path, err)
	}
	bounds := img.Bounds()
	debuglog.ImgFetch("decode: key=%s req_id=%d dur_ms=%d dims=(%d,%d)",
		req.Key, req.ReqID, time.Since(decStart).Milliseconds(),
		bounds.Dx(), bounds.Dy())

	if req.Target.X > 0 && req.Target.Y > 0 {
		img = downscale(img, req.Target)
	}

	// Populate the render-time memo so the UI thread's Cached() call
	// becomes a pure map lookup instead of os.Open + image.Decode +
	// downscale. Critical for keeping the bubbletea Update goroutine
	// responsive when many images arrive in a burst (channel switch
	// or scroll-up into unseen history).
	f.decoded.Store(decodedMemoKey(req.Key, req.Target), img)

	// Eagerly run protocol encoding off the UI thread so the next
	// View() doesn't have to. Skipped when not configured or when
	// CellTarget is zero (e.g., avatars and full-screen preview).
	prStart := time.Now()
	f.maybePrerender(req.Key, img, req.CellTarget)
	debuglog.ImgFetch("prerender: key=%s req_id=%d cell_target=(%d,%d) dur_ms=%d",
		req.Key, req.ReqID, req.CellTarget.X, req.CellTarget.Y,
		time.Since(prStart).Milliseconds())

	mime := mimeFromExt(filepath.Ext(path))
	return FetchResult{Img: img, Source: path, Mime: mime}, nil
}

// maybePrerender runs the active protocol's RenderImage on the
// just-decoded image and stashes the result. No-op when prerender is
// not configured or cellT is zero. Called from the fetch goroutine.
//
// Kitty is special: SetSource + RenderKey go through the package-level
// KittyRenderer mutex which is also held briefly by the UI thread's
// fallback path; both paths are mutually safe.
func (f *Fetcher) maybePrerender(key string, img image.Image, cellT image.Point) {
	f.prerenderMu.RLock()
	proto := f.prerenderProto
	kr := f.prerenderKitty
	m := f.prerendered
	f.prerenderMu.RUnlock()

	if proto == ProtoOff || cellT.X <= 0 || cellT.Y <= 0 {
		return
	}
	if m == nil {
		return // not configured yet
	}

	var r Render
	switch proto {
	case ProtoKitty:
		if kr == nil {
			return
		}
		ckey := "F-" + key // mirrors renderAttachmentBlock's stable kitty source key
		kr.SetSource(ckey, img)
		r = kr.RenderKey(ckey, cellT)
	default:
		r = RenderImage(proto, img, cellT)
	}
	m.Store(prerenderKey(key, cellT, proto), r)
}

// download fetches url and returns its body bytes + Content-Type.
// HTTP 429 responses are retried with exponential backoff up to
// rateLimitMaxRetries times before giving up.
func (f *Fetcher) download(ctx context.Context, url string) (body []byte, contentType string, err error) {
	body, ct, status, err := f.tryDownloadWithBackoff(ctx, url)
	if err != nil {
		return nil, "", err
	}
	if status != http.StatusOK {
		return nil, "", fmt.Errorf("fetch %s: HTTP %d", url, status)
	}
	if !strings.HasPrefix(strings.ToLower(ct), "image/") {
		return nil, "", fmt.Errorf("fetch %s: unexpected content type %q", url, ct)
	}
	return body, ct, nil
}

// tryDownloadWithBackoff issues one logical fetch with rate-limit
// retry. Returns (body, content-type, status, err). On HTTP 429 the
// request is retried with exponential backoff up to rateLimitMaxRetries
// times. On any other terminal status (success or non-429 failure) it
// returns immediately.
func (f *Fetcher) tryDownloadWithBackoff(ctx context.Context, url string) ([]byte, string, int, error) {
	backoff := rateLimitInitialBackoff
	for attempt := 0; attempt <= rateLimitMaxRetries; attempt++ {
		body, ct, status, err := f.tryDownload(ctx, url)
		if status != http.StatusTooManyRequests {
			return body, ct, status, err
		}
		if attempt == rateLimitMaxRetries {
			return body, ct, status, fmt.Errorf("fetch %s: HTTP 429 after %d retries", url, attempt+1)
		}
		debuglog.ImgFetch("HTTP 429 for %s (attempt %d/%d); backing off %s",
			url, attempt+1, rateLimitMaxRetries+1, backoff)
		select {
		case <-ctx.Done():
			return nil, "", 0, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > rateLimitMaxBackoff {
			backoff = rateLimitMaxBackoff
		}
	}
	return nil, "", 0, fmt.Errorf("fetch %s: unreachable", url)
}

// tryDownload issues a single HTTP GET through the injected client.
// Returns (body, content-type, status-code, err).
// Body is nil for non-200 responses; caller decides whether to treat
// them as terminal or retry.
func (f *Fetcher) tryDownload(ctx context.Context, url string) ([]byte, string, int, error) {
	httpStart := time.Now()
	debuglog.ImgFetch("http-try: url=%s", url)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		debuglog.ImgFetch("http-result: url=%s dur_ms=%d newrequest_err=%v",
			url, time.Since(httpStart).Milliseconds(), err)
		return nil, "", 0, err
	}
	resp, err := f.http.Do(httpReq)
	if err != nil {
		debuglog.ImgFetch("http-result: url=%s dur_ms=%d transport_err=%v",
			url, time.Since(httpStart).Milliseconds(), err)
		return nil, "", 0, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK {
		// Drain body for connection reuse, but we don't return it.
		_, _ = io.Copy(io.Discard, resp.Body)
		debuglog.ImgFetch("http-result: url=%s status=%d ct=%q dur_ms=%d body_drained",
			url, resp.StatusCode, ct, time.Since(httpStart).Milliseconds())
		return nil, ct, resp.StatusCode, nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		debuglog.ImgFetch("http-result: url=%s status=%d ct=%q dur_ms=%d body_read_err=%v",
			url, resp.StatusCode, ct, time.Since(httpStart).Milliseconds(), err)
		return nil, ct, resp.StatusCode, err
	}
	debuglog.ImgFetch("http-result: url=%s status=%d ct=%q dur_ms=%d bytes=%d",
		url, resp.StatusCode, ct, time.Since(httpStart).Milliseconds(), len(body))
	return body, ct, resp.StatusCode, nil
}

// downscale fits img within target preserving the renderer's expectation;
// the renderer always wants exactly target pixels — so we always scale.
// (Avoids an extra branch and image-copy path.)
func downscale(img image.Image, target image.Point) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, target.X, target.Y))
	draw.BiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)
	return dst
}

func extFromMime(contentType, url string) string {
	ct := strings.ToLower(contentType)
	switch {
	case strings.HasPrefix(ct, "image/png"):
		return "png"
	case strings.HasPrefix(ct, "image/jpeg"), strings.HasPrefix(ct, "image/jpg"):
		return "jpg"
	case strings.HasPrefix(ct, "image/gif"):
		return "gif"
	}
	// Fall back to URL extension.
	if i := strings.LastIndex(url, "."); i >= 0 {
		ext := strings.ToLower(strings.TrimPrefix(url[i:], "."))
		if ext == "png" || ext == "jpg" || ext == "jpeg" || ext == "gif" {
			if ext == "jpeg" {
				return "jpg"
			}
			return ext
		}
	}
	return "png"
}

func mimeFromExt(ext string) string {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	}
	return "application/octet-stream"
}

// Bytes reads the cached file's raw bytes.
func (f *Fetcher) Bytes(key string) ([]byte, error) {
	path, ok := f.cache.Get(key)
	if !ok {
		return nil, fmt.Errorf("not cached: %s", key)
	}
	return os.ReadFile(path)
}

// Cached returns the decoded image and true if and only if a memo
// entry for (key, target) is already in memory. Pure sync.Map lookup;
// never touches the disk, never starts a network download, never
// blocks on decode or downscale.
//
// Callers that need to render an image whose memo is cold MUST spawn
// a Fetch goroutine (see fetcher.Fetch / blockkit.fetchOrPlaceholder /
// imgrender.RenderBlock) and render a placeholder until the resulting
// ready-message lands and triggers a render-cache invalidation. The
// Fetch path handles the disk-cache hit case: when the file is
// already on disk, Fetch skips the HTTP semaphore (fetcher.go:226)
// and goes straight to decode + memo populate.
//
// This contract changed in May 2026 to fix a 3+ second freeze on the
// first channel visit of a new session: the previous behavior would
// open + image.Decode + downscale synchronously on the bubbletea
// Update goroutine for every memo miss with a disk hit -- ~100-200ms
// per image at typical Slack thumbnail sizes, summing to multi-second
// stalls on image-heavy channels. The async Fetch path that callers
// now drive achieves the same end state (decoded image memoized)
// without blocking the UI thread.
//
// The memo is populated by fetcher.Fetch (fetcher.go:314). When the
// underlying cache file is evicted (Cache.Delete or LRU eviction),
// stale memo entries may remain; callers see the stale image until
// a subsequent Fetch overwrites the entry. For our use case this is
// acceptable -- image-cache evictions are rare and the rendered
// content is content-addressable so a stale render is still correct.
func (f *Fetcher) Cached(key string, target image.Point) (image.Image, bool) {
	memoKey := decodedMemoKey(key, target)
	if v, ok := f.decoded.Load(memoKey); ok {
		if img, ok := v.(image.Image); ok {
			return img, true
		}
	}
	return nil, false
}

func decodedMemoKey(key string, target image.Point) string {
	return fmt.Sprintf("%s|%dx%d", key, target.X, target.Y)
}

// ThumbSpec is one provider thumbnail variant.
type ThumbSpec struct {
	URL string
	W   int
	H   int
}

// PickThumb selects the smallest thumb whose dimensions are >= target on
// both axes. Falls back to the largest available if none satisfy.
// suffix is a short string usable in cache keys (e.g. "720").
func PickThumb(thumbs []ThumbSpec, target image.Point) (url, suffix string) {
	if len(thumbs) == 0 {
		debuglog.ImgRender("PickThumb: no thumbs available target=(%d,%d)", target.X, target.Y)
		return "", ""
	}
	// Sort ascending by max(W, H).
	sorted := make([]ThumbSpec, len(thumbs))
	copy(sorted, thumbs)
	sort.Slice(sorted, func(i, j int) bool {
		return max(sorted[i].W, sorted[i].H) < max(sorted[j].W, sorted[j].H)
	})
	if debuglog.Enabled() {
		var b strings.Builder
		for i, t := range sorted {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, "(%dx%d)", t.W, t.H)
		}
		debuglog.ImgRender("PickThumb: target=(%d,%d) candidates=[%s]",
			target.X, target.Y, b.String())
	}
	for _, t := range sorted {
		if t.W >= target.X && t.H >= target.Y {
			debuglog.ImgRender("PickThumb: chose=(%d,%d) suffix=%d url=%s",
				t.W, t.H, max(t.W, t.H), t.URL)
			return t.URL, fmt.Sprintf("%d", max(t.W, t.H))
		}
	}
	last := sorted[len(sorted)-1]
	debuglog.ImgRender("PickThumb: chose=(%d,%d) suffix=%d url=%s (fallback: largest, no candidate satisfied target)",
		last.W, last.H, max(last.W, last.H), last.URL)
	return last.URL, fmt.Sprintf("%d", max(last.W, last.H))
}
