package webdriver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	cu "github.com/Davincible/chromedp-undetected"
	"github.com/chromedp/chromedp"

	"github.com/go-reader/reader/internal/config"
)

const chromeDefaultTimeout = 30 * time.Second

// ChromeDriver drives a real Chrome instance over the Chrome DevTools Protocol
// using chromedp-undetected, which patches the CDP launch so that common
// anti-bot checks (navigator.webdriver, --enable-automation banner, test-type
// flag, etc.) don't trip on the default chromedp configuration.
type ChromeDriver struct {
	mu         sync.Mutex
	browserCtx context.Context
	cancel     context.CancelFunc
	headless   bool
}

func NewChromeDriver() *ChromeDriver {
	return &ChromeDriver{}
}

// NewChromeHeadlessDriver drives Chrome in headless mode (no visible window),
// which is what servers and CI environments need.
func NewChromeHeadlessDriver() *ChromeDriver {
	return &ChromeDriver{headless: true}
}

func (d *ChromeDriver) Start(_ context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.browserCtx != nil {
		return nil
	}

	// NoSandbox=false avoids the --test-type flag that cu adds alongside
	// --no-sandbox, which itself is a detection signal on desktop OSes.
	// cu's own cu.WithHeadless() is not used: it spawns Xvfb and errors out on
	// Windows/macOS, so headless is requested through Chrome's own flag instead.
	// Headless always adds --no-sandbox: inside a container the SUID sandbox
	// needs CAP_SYS_ADMIN, which the default capability set does not grant, and
	// --disable-dev-shm-usage works around the default 64MB /dev/shm.
	opts := []cu.Option{
		cu.WithUserDataDir(config.Get().ChromeDataDir),
		cu.WithNoSandbox(false),
	}
	if d.headless {
		opts = append(opts, cu.WithChromeFlags(
			chromedp.Flag("headless", true),
			chromedp.Flag("no-sandbox", true),
			chromedp.Flag("disable-dev-shm-usage", true),
			chromedp.Flag("window-size", "1920,1080"),
			chromedp.Flag("hide-scrollbars", true),
			chromedp.Flag("mute-audio", true),
		))
	}
	cfg := cu.NewConfig(opts...)

	ctx, cancel, err := cu.New(cfg)
	if err != nil {
		return fmt.Errorf("start chrome: %w", err)
	}

	// Force-launch so a misconfigured user data dir or missing Chrome surfaces
	// here instead of on the first page load.
	if err := chromedp.Run(ctx, chromedp.Navigate("about:blank")); err != nil {
		cancel()
		return fmt.Errorf("start chrome: %w", err)
	}

	d.browserCtx = ctx
	d.cancel = cancel
	return nil
}

func (d *ChromeDriver) Stop(_ context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	d.browserCtx = nil
	return nil
}

func (d *ChromeDriver) session() (context.Context, context.CancelFunc, error) {
	d.mu.Lock()
	bctx := d.browserCtx
	d.mu.Unlock()
	if bctx == nil {
		return nil, nil, errors.New("chrome driver not started")
	}
	ctx, cancel := context.WithTimeout(bctx, chromeDefaultTimeout)
	return ctx, cancel, nil
}

func (d *ChromeDriver) Get(_ context.Context, url string) error {
	ctx, cancel, err := d.session()
	if err != nil {
		return err
	}
	defer cancel()
	return chromedp.Run(ctx, chromedp.Navigate(url))
}

func (d *ChromeDriver) CurrentURL(_ context.Context) (string, error) {
	ctx, cancel, err := d.session()
	if err != nil {
		return "", err
	}
	defer cancel()
	var u string
	if err := chromedp.Run(ctx, chromedp.Location(&u)); err != nil {
		return "", err
	}
	return u, nil
}

func (d *ChromeDriver) PageSource(_ context.Context) (string, error) {
	ctx, cancel, err := d.session()
	if err != nil {
		return "", err
	}
	defer cancel()
	var html string
	if err := chromedp.Run(ctx, chromedp.OuterHTML("html", &html, chromedp.ByQuery)); err != nil {
		return "", err
	}
	return html, nil
}

func (d *ChromeDriver) Title(_ context.Context) (string, error) {
	ctx, cancel, err := d.session()
	if err != nil {
		return "", err
	}
	defer cancel()
	var title string
	if err := chromedp.Run(ctx, chromedp.Title(&title)); err != nil {
		return "", err
	}
	return title, nil
}

func (d *ChromeDriver) ExecuteScript(_ context.Context, script string) (string, error) {
	ctx, cancel, err := d.session()
	if err != nil {
		return "", err
	}
	defer cancel()
	var raw any
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &raw)); err != nil {
		return "", err
	}
	switch v := raw.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v), nil
		}
		return string(b), nil
	}
}
