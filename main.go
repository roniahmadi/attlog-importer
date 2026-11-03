// attlog-importer: aplikasi desktop untuk mengirim file attlog mesin fingerprint
// ke endpoint /iclock/cdata ASIK, meniru request push mesin (User-Agent "iClock Proxy/1.09").
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"
)

const (
	userAgent    = "iClock Proxy/1.09" // UA mesin asli; server mengecek string "iClock Proxy"
	minDelaySec  = 0.5
	defaultBase  = "https://asik.trenggalekkab.go.id"
	defaultBatch = 20
	defaultDelay = 2.0
)

var (
	dtRe     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`)
	serialRe = regexp.MustCompile(`^([A-Za-z0-9]+)_attlog`)
)

type settings struct {
	file       string
	sn         string
	base       string
	batch      int
	startBatch int
	delay      time.Duration
	dryRun     bool
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// readRows membaca file attlog. Mengembalikan baris yang sudah dinormalisasi
// (PIN, waktu, status, verify, workcode, extra), total baris non-kosong, dan jumlah yang dilewati.
func readRows(path string) (rows []string, total, skipped int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		total++
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			skipped++
			continue
		}
		pin := strings.TrimSpace(parts[0])
		ts := strings.TrimSpace(parts[1])
		if !isDigits(pin) || !dtRe.MatchString(ts) {
			skipped++
			continue
		}
		rest := make([]string, 0, 4)
		for _, p := range parts[2:] {
			rest = append(rest, strings.TrimSpace(p))
		}
		for len(rest) < 4 {
			rest = append(rest, "0")
		}
		rows = append(rows, strings.Join(append([]string{pin, ts}, rest[:4]...), "\t"))
	}
	if err := sc.Err(); err != nil {
		return nil, total, skipped, err
	}
	return rows, total, skipped, nil
}

func postBatch(ctx context.Context, client *http.Client, target, body string) (int, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		return 0, "", "", err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", "", err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", "", err
	}
	var hb strings.Builder
	for k, vs := range resp.Header {
		for _, v := range vs {
			fmt.Fprintf(&hb, "%s: %s\n", k, v)
		}
	}
	return resp.StatusCode, hb.String(), string(b), nil
}

// runImport menjalankan proses impor. logf dan progress dipanggil dari goroutine ini.
func runImport(ctx context.Context, s settings, logf func(string), progress func(float64)) {
	rows, total, skipped, err := readRows(s.file)
	if err != nil {
		logf("ERROR membaca file: " + err.Error())
		return
	}
	logf(fmt.Sprintf("Baris total: %d | valid: %d | dilewati (PIN non-angka/format salah): %d", total, len(rows), skipped))

	var batches [][]string
	for i := 0; i < len(rows); i += s.batch {
		end := i + s.batch
		if end > len(rows) {
			end = len(rows)
		}
		batches = append(batches, rows[i:end])
	}
	logf(fmt.Sprintf("Batch: %d x maks %d baris", len(batches), s.batch))

	if s.dryRun {
		logf("DRY RUN: tidak ada request yang dikirim. Contoh 3 baris pertama:")
		for i, r := range rows {
			if i >= 3 {
				break
			}
			logf("  " + strings.ReplaceAll(r, "\t", " | "))
		}
		progress(1)
		return
	}

	u, err := url.Parse(strings.TrimRight(s.base, "/") + "/iclock/cdata")
	if err != nil {
		logf("ERROR base URL: " + err.Error())
		return
	}
	q := u.Query()
	q.Set("SN", s.sn)
	q.Set("table", "ATTLOG")
	u.RawQuery = q.Encode()
	target := u.String()

	client := &http.Client{Timeout: 120 * time.Second}
	ok := 0
	var failed []int

	for i, b := range batches {
		idx := i + 1
		if idx < s.startBatch {
			continue
		}
		if ctx.Err() != nil {
			logf("Dihentikan oleh pengguna.")
			break
		}
		body := strings.Join(b, "\n") + "\n"
		logf(fmt.Sprintf("=== [%d/%d] POST %s (%d baris)", idx, len(batches), target, len(b)))

		status, hdr, txt, err := postBatch(ctx, client, target, body)
		if err != nil {
			status = 0
			txt = "EXCEPTION: " + err.Error()
		}
		logf(fmt.Sprintf("--- status: %d\n--- headers:\n%s--- body:\n%s",
			status, hdr, strings.TrimRight(txt, "\n")))

		if status >= 200 && status < 300 {
			ok++
		} else {
			failed = append(failed, idx)
		}
		progress(float64(idx) / float64(len(batches)))

		select {
		case <-ctx.Done():
		case <-time.After(s.delay):
		}
	}

	summary := fmt.Sprintf("Selesai. Batch OK: %d, gagal: ", ok)
	if len(failed) == 0 {
		summary += "tidak ada"
	} else {
		summary += fmt.Sprint(failed)
	}
	logf(summary)
}

func main() {
	a := app.New()
	w := a.NewWindow("Importer Attlog ASIK")
	w.Resize(fyne.NewSize(860, 680))

	fileEntry := widget.NewEntry()
	fileEntry.SetPlaceHolder("Pilih file *_attlog.dat")
	snEntry := widget.NewEntry()
	snEntry.SetPlaceHolder("Serial mesin, mis. BJ36185160265")
	baseEntry := widget.NewEntry()
	baseEntry.SetText(defaultBase)
	batchEntry := widget.NewEntry()
	batchEntry.SetText(strconv.Itoa(defaultBatch))
	delayEntry := widget.NewEntry()
	delayEntry.SetText(strconv.FormatFloat(defaultDelay, 'f', -1, 64))
	startEntry := widget.NewEntry()
	startEntry.SetText("1")
	dryCheck := widget.NewCheck("Dry run (tidak mengirim apa pun)", nil)
	dryCheck.SetChecked(true)

	pickBtn := widget.NewButton("Pilih file...", func() {
		d := dialog.NewFileOpen(func(rc fyne.URIReadCloser, err error) {
			if err != nil {
				dialog.ShowError(err, w)
				return
			}
			if rc == nil {
				return
			}
			path := rc.URI().Path()
			rc.Close()
			fileEntry.SetText(path)
			if m := serialRe.FindStringSubmatch(filepath.Base(path)); m != nil {
				snEntry.SetText(m[1])
			}
		}, w)
		d.SetFilter(storage.NewExtensionFileFilter([]string{".dat", ".txt"}))
		d.Show()
	})

	logBind := binding.NewString()
	progressBind := binding.NewFloat()
	logLabel := widget.NewLabelWithData(logBind)
	logLabel.Wrapping = fyne.TextWrapWord
	bar := widget.NewProgressBarWithData(progressBind)

	logf := func(s string) {
		cur, _ := logBind.Get()
		if cur == "" {
			_ = logBind.Set(s)
		} else {
			_ = logBind.Set(cur + "\n" + s)
		}
	}

	startBtn := widget.NewButton("Mulai", nil)
	stopBtn := widget.NewButton("Hentikan", nil)
	stopBtn.Disable()
	var cancel context.CancelFunc

	collect := func() (settings, error) {
		var s settings
		s.file = strings.TrimSpace(fileEntry.Text)
		if s.file == "" {
			return s, fmt.Errorf("pilih file dulu")
		}
		if _, err := os.Stat(s.file); err != nil {
			return s, fmt.Errorf("file tidak bisa dibaca: %v", err)
		}
		s.sn = strings.TrimSpace(snEntry.Text)
		if s.sn == "" {
			return s, fmt.Errorf("serial mesin tidak boleh kosong")
		}
		s.base = strings.TrimSpace(baseEntry.Text)
		bu, err := url.Parse(s.base)
		if err != nil || (bu.Scheme != "http" && bu.Scheme != "https") || bu.Host == "" {
			return s, fmt.Errorf("base URL tidak valid: %q", s.base)
		}
		b, err := strconv.Atoi(strings.TrimSpace(batchEntry.Text))
		if err != nil || b < 1 {
			return s, fmt.Errorf("batch harus angka >= 1")
		}
		s.batch = b
		st, err := strconv.Atoi(strings.TrimSpace(startEntry.Text))
		if err != nil || st < 1 {
			return s, fmt.Errorf("mulai dari batch harus angka >= 1")
		}
		s.startBatch = st
		d, err := strconv.ParseFloat(strings.TrimSpace(delayEntry.Text), 64)
		if err != nil || d < minDelaySec {
			return s, fmt.Errorf("jeda minimal %.1f detik", minDelaySec)
		}
		s.delay = time.Duration(d * float64(time.Second))
		s.dryRun = dryCheck.Checked
		return s, nil
	}

	var start func(s settings)
	start = func(s settings) {
		startBtn.Disable()
		stopBtn.Enable()
		_ = logBind.Set("")
		_ = progressBind.Set(0)
		ctx, c := context.WithCancel(context.Background())
		cancel = c
		go func() {
			runImport(ctx, s, logf, func(p float64) { _ = progressBind.Set(p) })
			c()
			fyne.Do(func() {
				startBtn.Enable()
				stopBtn.Disable()
			})
		}()
	}

	startBtn.OnTapped = func() {
		s, err := collect()
		if err != nil {
			dialog.ShowError(err, w)
			return
		}
		if s.dryRun {
			start(s)
			return
		}
		msg := fmt.Sprintf("Kirim %s ke %s untuk mesin %s?\nBatch %d baris, jeda %s.",
			filepath.Base(s.file), s.base, s.sn, s.batch, s.delay)
		dialog.ShowConfirm("Konfirmasi kirim", msg, func(yes bool) {
			if yes {
				start(s)
			}
		}, w)
	}
	stopBtn.OnTapped = func() {
		if cancel != nil {
			cancel()
		}
	}

	form := widget.NewForm(
		&widget.FormItem{Text: "File attlog", Widget: container.NewBorder(nil, nil, nil, pickBtn, fileEntry)},
		&widget.FormItem{Text: "Serial mesin", Widget: snEntry},
		&widget.FormItem{Text: "Base URL", Widget: baseEntry},
		&widget.FormItem{Text: "Baris per batch", Widget: batchEntry},
		&widget.FormItem{Text: "Jeda (detik)", Widget: delayEntry},
		&widget.FormItem{Text: "Mulai dari batch", Widget: startEntry},
		&widget.FormItem{Text: "Mode", Widget: dryCheck},
	)

	top := container.NewVBox(
		form,
		container.NewHBox(startBtn, stopBtn),
		bar,
	)
	w.SetContent(container.NewBorder(top, nil, nil, nil, container.NewVScroll(logLabel)))
	w.ShowAndRun()
}
