package webdriver

// New returns a Driver chosen by the CHROME_DRIVER config value.
// "none" uses plain HTTP; "chrome" drives a real Chrome instance over the
// Chrome DevTools Protocol via chromedp, while "chrome-headless" does the same
// without a visible browser window.
func New(name string) Driver {
	switch name {
	case "none":
		return NewNoneDriver()
	case "chrome":
		return NewChromeDriver()
	case "chrome-headless":
		return NewChromeHeadlessDriver()
	default:
		return NewNoneDriver()
	}
}
