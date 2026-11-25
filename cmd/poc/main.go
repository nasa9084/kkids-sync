// Command poc is a throwaway tool for studying the Amazon Kids parent
// dashboard before the real reconciler is written.
//
//	poc login    open a browser, sign in by hand, save the session
//	poc inspect  reuse the session, browse by hand while everything is recorded
//
// Nothing here clicks or changes anything on its own. In inspect mode the
// browser also refuses state-changing requests to the dashboard, so toggles
// cannot take effect by accident.
//
// Everything is written under -dir (default .poc/), which is git-ignored.
// The session file and the HAR contain live cookies: never commit or share them.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/nasa9084/kkids-sync/internal/guard"
)

const defaultStart = "https://parents.amazon.co.jp/"

func main() {
	dir := flag.String("dir", ".poc", "output directory (git-ignored)")
	start := flag.String("start", defaultStart, "page to open first")
	install := flag.Bool("install", false, "download the Playwright driver and Chromium, then exit")
	allowWrites := flag.Bool("allow-writes", false, "inspect: let dashboard writes through (toggles take effect) and record them")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: poc [flags] login|inspect\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *install {
		// Headful only: skip the headless shell to keep the download small.
		if err := playwright.Install(&playwright.RunOptions{Browsers: []string{"chromium"}, NoInstallShell: true}); err != nil {
			log.Fatalf("install: %v", err)
		}
		return
	}
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	mode := flag.Arg(0)
	if mode != "login" && mode != "inspect" {
		flag.Usage()
		os.Exit(2)
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		log.Fatal(err)
	}
	if err := run(mode, *dir, *start, *allowWrites); err != nil {
		log.Fatal(err)
	}
}

func run(mode, dir, start string, allowWrites bool) error {
	statePath := filepath.Join(dir, "storage-state.json")
	stamp := time.Now().Format("20060102-150405")
	policy := guard.Policy{ReadOnly: mode == "inspect" && !allowWrites}
	if allowWrites {
		log.Printf("WRITES ALLOWED: toggles in the dashboard take effect")
	}

	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("start playwright (run with -install first?): %w", err)
	}
	defer pw.Stop()

	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(false),
	})
	if err != nil {
		return fmt.Errorf("launch chromium: %w", err)
	}
	defer browser.Close()

	opts := playwright.BrowserNewContextOptions{
		Locale:     playwright.String("ja-JP"),
		TimezoneId: playwright.String("Asia/Tokyo"),
	}
	if mode == "inspect" {
		if _, err := os.Stat(statePath); err != nil {
			return fmt.Errorf("no saved session at %s: run `poc login` first", statePath)
		}
		opts.StorageStatePath = playwright.String(statePath)
		opts.RecordHarPath = playwright.String(filepath.Join(dir, "network-"+stamp+".har"))
		opts.RecordHarContent = playwright.HarContentPolicyOmit
	}
	ctx, err := browser.NewContext(opts)
	if err != nil {
		return fmt.Errorf("new context: %w", err)
	}

	rec := newRecorder()
	captured := &capture{dir: dir, stamp: stamp}
	if mode == "inspect" {
		// Keep the bodies of the book-list API so we can see what identifies a
		// title (ASIN or otherwise) and how paging works. Body() blocks, so it
		// runs off the event goroutine.
		ctx.OnResponse(func(resp playwright.Response) {
			if !captureAPI.MatchString(pathOf(resp.URL())) {
				return
			}
			reqBody, _ := resp.Request().PostData()
			captured.wg.Add(1)
			go func() {
				defer captured.wg.Done()
				body, err := resp.Body()
				if err != nil {
					log.Printf("read body of %s: %v", resp.URL(), err)
					return
				}
				name := strings.TrimPrefix(pathOf(resp.URL()), "/ajax/")
				captured.save("api-"+name+"-req", reqBody, "json")
				captured.save("api-"+name+"-resp", string(body), "json")
			}()
		})
	}
	if err := ctx.Route("**/*", func(route playwright.Route) {
		req := route.Request()
		d, why := policy.Check(req.URL(), req.Method(), req.IsNavigationRequest())
		rec.add(req.Method(), req.ResourceType(), req.URL(), d, why)
		if d == guard.Block {
			log.Printf("BLOCKED %s %s (%s)", req.Method(), req.URL(), why)
			if body, err := req.PostData(); err == nil && body != "" {
				captured.save("blocked", fmt.Sprintf("%s %s\n\n%s\n", req.Method(), stripQuery(req.URL()), body), "txt")
			}
			_ = route.Abort("blockedbyclient")
			return
		}
		if allowWrites && isDashboardWrite(req.URL(), req.Method()) {
			if body, err := req.PostData(); err == nil {
				captured.save("write", fmt.Sprintf("%s %s\n\n%s\n", req.Method(), stripQuery(req.URL()), body), "txt")
			}
			log.Printf("WRITE %s %s", req.Method(), stripQuery(req.URL()))
		}
		_ = route.Continue()
	}); err != nil {
		return fmt.Errorf("route: %w", err)
	}

	tracePath := filepath.Join(dir, "trace-"+stamp+".zip")
	if mode == "inspect" {
		if err := ctx.Tracing().Start(playwright.TracingStartOptions{
			Screenshots: playwright.Bool(true),
			Snapshots:   playwright.Bool(true),
		}); err != nil {
			return fmt.Errorf("start tracing: %w", err)
		}
	}

	page, err := ctx.NewPage()
	if err != nil {
		return fmt.Errorf("new page: %w", err)
	}
	if _, err := page.Goto(start); err != nil {
		log.Printf("goto %s: %v (carry on in the browser)", start, err)
	}

	switch mode {
	case "login":
		waitEnter("ブラウザでサインインし、保護者ダッシュボードが表示されたら Enter を押してください")
	case "inspect":
		if allowWrites {
			waitEnter("書き込みが有効です。トグルは実際に反映されます。\n" +
				"本の一覧で、今オフになっている本を1冊選び、トグルをオンにして数秒待ち、またオフに戻してください。\n" +
				"終わったら Enter を押してください")
		} else {
			waitEnter("保護者ダッシュボードで、子どもプロフィール → コンテンツ管理 → Amazonコンテンツ(本)の一覧まで開いてください。\n" +
				"一覧のスクロールやページ送り、検索も試してください(トグルは押しても反映されません)。\n" +
				"表示が欠けるときは、ターミナルの BLOCKED の行を控えてください。\n" +
				"トグルを1つだけ押してみてください(送ろうとした内容が blocked-*.txt に残ります)。\n" +
				"終わったら、本の一覧を表示したまま Enter を押してください")
		}
		if err := dumpPages(ctx, dir, stamp); err != nil {
			log.Printf("dump pages: %v", err)
		}
		captured.wg.Wait()
		if err := ctx.Tracing().Stop(tracePath); err != nil {
			log.Printf("stop tracing: %v", err)
		}
		if err := rec.write(filepath.Join(dir, "requests-"+stamp+".tsv")); err != nil {
			log.Printf("write requests: %v", err)
		}
	}

	// Saving refreshes the cookies the site rotated during this run.
	if _, err := ctx.StorageState(playwright.BrowserContextStorageStateOptions{Path: playwright.String(statePath)}); err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	if err := os.Chmod(statePath, 0o600); err != nil {
		return err
	}
	if err := ctx.Close(); err != nil { // flushes the HAR
		return fmt.Errorf("close context: %w", err)
	}
	log.Printf("saved under %s", dir)
	return nil
}

// dumpPages writes the HTML and a full-page screenshot of every open tab.
func dumpPages(ctx playwright.BrowserContext, dir, stamp string) error {
	for i, p := range ctx.Pages() {
		base := filepath.Join(dir, fmt.Sprintf("page-%s-%d", stamp, i))
		html, err := p.Content()
		if err != nil {
			return err
		}
		if err := os.WriteFile(base+".html", []byte(html), 0o600); err != nil {
			return err
		}
		if _, err := p.Screenshot(playwright.PageScreenshotOptions{
			Path:     playwright.String(base + ".png"),
			FullPage: playwright.Bool(true),
		}); err != nil {
			return err
		}
		log.Printf("dumped %s (%s)", base, p.URL())
	}
	return nil
}

func waitEnter(msg string) {
	fmt.Println()
	fmt.Println(msg)
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

// recorder keeps one line per request so the traffic shape can be reviewed
// without opening the HAR (which holds cookies).
type recorder struct {
	mu    sync.Mutex
	lines []string
	hosts map[string]int
}

func newRecorder() *recorder { return &recorder{hosts: map[string]int{}} }

func (r *recorder) add(method, typ, rawURL string, d guard.Decision, why string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u := rawURL
	if i := strings.IndexByte(u, '?'); i >= 0 {
		u = u[:i] // query strings may carry tokens
	}
	r.lines = append(r.lines, strings.Join([]string{time.Now().Format(time.RFC3339), method, typ, d.String(), why, u}, "\t"))
	host := u
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	r.hosts[host]++
}

func (r *recorder) write(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	b.WriteString("# time\tmethod\ttype\tdecision\treason\turl(no query)\n")
	for _, l := range r.lines {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n# requests per host\n")
	hosts := make([]string, 0, len(r.hosts))
	for h := range r.hosts {
		hosts = append(hosts, h)
	}
	sort.Slice(hosts, func(i, j int) bool { return r.hosts[hosts[i]] > r.hosts[hosts[j]] })
	for _, h := range hosts {
		fmt.Fprintf(&b, "%d\t%s\n", r.hosts[h], h)
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// captureAPI matches the dashboard endpoints whose responses are saved.
var captureAPI = regexp.MustCompile(`^/ajax/(get|search)-additional-content$`)

// capture writes numbered files under the output directory.
type capture struct {
	dir, stamp string
	n          atomic.Int64
	wg         sync.WaitGroup
}

func (c *capture) save(kind, content, ext string) {
	n := c.n.Add(1)
	path := filepath.Join(c.dir, fmt.Sprintf("%s-%s-%03d.%s", kind, c.stamp, n, ext))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		log.Printf("write %s: %v", path, err)
	}
}

func pathOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Path
}

func stripQuery(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
}

// isDashboardWrite reports a state-changing request to the parent dashboard
// that is not one of the read-style endpoints.
func isDashboardWrite(raw, method string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() != "parents.amazon.co.jp" {
		return false
	}
	if method == "GET" || method == "HEAD" || method == "OPTIONS" {
		return false
	}
	d, _ := guard.Policy{ReadOnly: true}.Check(raw, method, false)
	return d == guard.Block
}
