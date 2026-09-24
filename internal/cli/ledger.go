package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The spend ledger records what every tool spent, per day, so daily and
// monthly caps hold across invocations. It is one small text file:
//
//	# grev spend ledger: day<TAB>tool<TAB>usd<TAB>tokens
//	2026-09-24	grev	0.016912	402118
//
// Writers take a lock file (portable: create-exclusive, stale after 10s).
// Parallel invocations can overshoot a cap by what they already have in
// flight.

// LedgerEntry is one day's spend by one tool.
type LedgerEntry struct {
	Day    string // YYYY-MM-DD, local time
	Tool   string
	Cost   float64
	Tokens int
}

// Ledger is the spend ledger as seen by one tool invocation.
type Ledger struct {
	Path           string
	Tool           string
	Daily, Monthly float64 // caps in USD; 0 = none

	mu        sync.Mutex
	cachedAt  time.Time
	day, mon  float64
	cacheDay  string
	lastError error
}

const (
	ledgerKeepDays = 62
	lockStale      = 10 * time.Second
	lockWait       = 5 * time.Second
)

// LedgerPath is where the ledger lives: GREV_LEDGER if set (empty turns the
// ledger off), else ${XDG_STATE_HOME:-~/.local/state}/grev/spend, or
// %LocalAppData%\grev\spend on Windows.
func LedgerPath() string {
	if p, ok := os.LookupEnv("GREV_LEDGER"); ok {
		return p
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LocalAppData"); d != "" {
			return filepath.Join(d, "grev", "spend")
		}
		if d, err := os.UserCacheDir(); err == nil {
			return filepath.Join(d, "grev", "spend")
		}
		return ""
	}
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "grev", "spend")
}

// OpenLedger returns the ledger for tool, or nil when it is turned off.
func OpenLedger(tool string, daily, monthly float64) *Ledger {
	p := LedgerPath()
	if p == "" {
		return nil
	}
	return &Ledger{Path: p, Tool: tool, Daily: daily, Monthly: monthly}
}

// Capped reports whether any cap is set.
func (l *Ledger) Capped() bool { return l != nil && (l.Daily > 0 || l.Monthly > 0) }

// Add records spend for today.
func (l *Ledger) Add(cost float64, tokens int) {
	if l == nil || (cost == 0 && tokens == 0) {
		return
	}
	day := today()
	err := l.locked(func() error {
		entries, err := ReadLedger(l.Path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		found := false
		for i := range entries {
			if entries[i].Day == day && entries[i].Tool == l.Tool {
				entries[i].Cost += cost
				entries[i].Tokens += tokens
				found = true
			}
		}
		if !found {
			entries = append(entries, LedgerEntry{Day: day, Tool: l.Tool, Cost: cost, Tokens: tokens})
		}
		cutoff := time.Now().AddDate(0, 0, -ledgerKeepDays).Format("2006-01-02")
		kept := entries[:0]
		for _, e := range entries {
			if e.Day >= cutoff {
				kept = append(kept, e)
			}
		}
		return writeLedger(l.Path, kept)
	})
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		l.lastError = err
		return
	}
	l.cachedAt = time.Time{} // force a re-read for the next Remaining
}

// Spent returns today's and this month's spend across all tools.
func (l *Ledger) Spent() (day, month float64, err error) {
	entries, err := ReadLedger(l.Path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, 0, err
	}
	d, m := today(), today()[:7]
	for _, e := range entries {
		if e.Day == d {
			day += e.Cost
		}
		if strings.HasPrefix(e.Day, m) {
			month += e.Cost
		}
	}
	return day, month, nil
}

// Remaining is the USD still allowed by the caps (the smaller of the daily
// and monthly headroom), or -1 when no cap is set. Reads are cached for a
// second.
func (l *Ledger) Remaining() float64 {
	if !l.Capped() {
		return -1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Since(l.cachedAt) > time.Second || l.cacheDay != today() {
		day, mon, err := l.Spent()
		if err != nil {
			l.lastError = err
			return 0 // can't read the ledger: fail closed
		}
		l.day, l.mon, l.cachedAt, l.cacheDay = day, mon, time.Now(), today()
	}
	rem := -1.0
	if l.Daily > 0 {
		rem = max(0, l.Daily-l.day)
	}
	if l.Monthly > 0 {
		if r := max(0, l.Monthly-l.mon); rem < 0 || r < rem {
			rem = r
		}
	}
	return rem
}

// Describe summarizes the caps and what is left, for messages.
func (l *Ledger) Describe() string {
	if !l.Capped() {
		return ""
	}
	day, mon, _ := l.Spent()
	var parts []string
	if l.Daily > 0 {
		parts = append(parts, fmt.Sprintf("daily cap %s, %s spent today", fmtCost(l.Daily), fmtCost(day)))
	}
	if l.Monthly > 0 {
		parts = append(parts, fmt.Sprintf("monthly cap %s, %s spent this month", fmtCost(l.Monthly), fmtCost(mon)))
	}
	return strings.Join(parts, "; ")
}

// Err is the last ledger error, if any (reported once at exit).
func (l *Ledger) Err() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastError
}

func today() string { return time.Now().Format("2006-01-02") }

// ReadLedger reads every entry, sorted by day then tool.
func ReadLedger(path string) ([]LedgerEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []LedgerEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 4 {
			continue
		}
		cost, err1 := strconv.ParseFloat(f[2], 64)
		tok, err2 := strconv.Atoi(f[3])
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, LedgerEntry{Day: f[0], Tool: f[1], Cost: cost, Tokens: tok})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Day != out[j].Day {
			return out[i].Day < out[j].Day
		}
		return out[i].Tool < out[j].Tool
	})
	return out, sc.Err()
}

func writeLedger(path string, entries []LedgerEntry) error {
	var b strings.Builder
	b.WriteString("# grev spend ledger: day\ttool\tusd\ttokens\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "%s\t%s\t%.6f\t%d\n", e.Day, e.Tool, e.Cost, e.Tokens)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".spend.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// locked runs fn holding the ledger's lock file.
func (l *Ledger) locked(fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o700); err != nil {
		return err
	}
	lock := l.Path + ".lock"
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			defer os.Remove(lock)
			return fn()
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if fi, err := os.Stat(lock); err == nil && time.Since(fi.ModTime()) > lockStale {
			os.Remove(lock) // a crashed writer
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ledger %s is locked (remove %s if no grev tool is running)", l.Path, lock)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
