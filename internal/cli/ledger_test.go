package cli

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLedgerAddAndCaps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "spend")
	t.Setenv("GREV_LEDGER", path)
	l := OpenLedger("grev", 0.10, 1.00)
	if l.Remaining() != 0.10 {
		t.Fatalf("empty ledger remaining = %v", l.Remaining())
	}
	l.Add(0.03, 1000)
	l.Add(0.02, 500)
	other := OpenLedger("seek", 0, 0)
	other.Add(0.01, 100)
	if other.Remaining() != -1 {
		t.Fatal("no caps should mean no limit")
	}
	day, mon, err := l.Spent()
	if err != nil || !near(day, 0.06) || !near(mon, 0.06) {
		t.Fatalf("spent %v %v %v", day, mon, err)
	}
	time.Sleep(1100 * time.Millisecond) // Remaining caches for a second
	if r := l.Remaining(); !near(r, 0.04) {
		t.Fatalf("remaining %v", r)
	}
	entries, _ := ReadLedger(path)
	if len(entries) != 2 || entries[0].Tool != "grev" || entries[0].Tokens != 1500 {
		t.Fatalf("entries %+v", entries)
	}
	if !strings.Contains(l.Describe(), "daily cap $0.1000") {
		t.Fatalf("describe: %s", l.Describe())
	}
}

func TestLedgerPruneAndMonth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spend")
	t.Setenv("GREV_LEDGER", path)
	now := time.Now()
	old := now.AddDate(0, 0, -90).Format("2006-01-02")
	earlier := now.AddDate(0, 0, -1)
	lines := "# header\n" + old + "\tgrev\t9.000000\t1\n"
	if earlier.Format("2006-01") == now.Format("2006-01") {
		lines += earlier.Format("2006-01-02") + "\tgrev\t0.500000\t10\n"
	}
	lines += "garbage line\n"
	os.WriteFile(path, []byte(lines), 0o600)
	l := OpenLedger("grev", 0, 1.0)
	l.Add(0.1, 1)
	entries, _ := ReadLedger(path)
	for _, e := range entries {
		if e.Day == old {
			t.Fatal("entries older than 62 days should be pruned")
		}
	}
	_, mon, _ := l.Spent()
	want := 0.1
	if earlier.Format("2006-01") == now.Format("2006-01") {
		want = 0.6
	}
	if !near(mon, want) {
		t.Fatalf("month spend %v, want %v", mon, want)
	}
}

func TestLedgerConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spend")
	t.Setenv("GREV_LEDGER", path)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := OpenLedger("grev", 0, 0)
			for j := 0; j < 25; j++ {
				l.Add(0.001, 1)
			}
			if err := l.Err(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	entries, _ := ReadLedger(path)
	if len(entries) != 1 || entries[0].Tokens != 200 || !near(entries[0].Cost, 0.2) {
		t.Fatalf("lost updates: %+v", entries)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatal("lock file left behind")
	}
}

func TestLedgerStaleLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spend")
	t.Setenv("GREV_LEDGER", path)
	os.WriteFile(path+".lock", nil, 0o600)
	past := time.Now().Add(-time.Minute)
	os.Chtimes(path+".lock", past, past)
	l := OpenLedger("grev", 0, 0)
	l.Add(0.01, 1)
	if l.Err() != nil {
		t.Fatalf("stale lock not broken: %v", l.Err())
	}
}

func TestLedgerOff(t *testing.T) {
	t.Setenv("GREV_LEDGER", "")
	if OpenLedger("grev", 1, 1) != nil {
		t.Fatal("empty GREV_LEDGER should turn the ledger off")
	}
	var l *Ledger
	l.Add(1, 1) // nil-safe
	if l.remaining() != -1 || l.Capped() || l.Err() != nil {
		t.Fatal("nil ledger should be inert")
	}
}

func near(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }
