package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/karanmonu/flakewatch/internal/gh"
)

var epoch = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

func run(id int64, started time.Time) gh.WorkflowRun {
	return gh.WorkflowRun{
		ID:           id,
		Name:         "CI",
		Path:         ".github/workflows/ci.yml",
		Status:       "completed",
		Conclusion:   "success",
		RunStartedAt: started,
	}
}

func job(d time.Duration) gh.Job {
	return gh.Job{Labels: []string{"ubuntu-latest"}, StartedAt: epoch, CompletedAt: epoch.Add(d)}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()

	c, err := Open(dir, "owner/name")
	if err != nil {
		t.Fatal(err)
	}
	c.Put(run(1, time.Now().Add(-time.Hour)), []gh.Job{job(time.Minute)})
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir, "owner/name")
	if err != nil {
		t.Fatal(err)
	}
	jobs, ok := reopened.Jobs(1)
	if !ok || len(jobs) != 1 {
		t.Fatalf("run 1 should have survived the round trip, got %v %v", jobs, ok)
	}
	if !jobs[0].CompletedAt.Equal(epoch.Add(time.Minute)) {
		t.Fatalf("timestamps did not survive encoding: %v", jobs[0].CompletedAt)
	}
}

// The premise the whole package rests on. An in-progress run's jobs are still
// growing, and nothing ever re-fetches a run already in the cache -- so
// storing one would freeze a half-measured run into history permanently.
func TestOnlyCompletedRunsAreStored(t *testing.T) {
	c, _ := Open(t.TempDir(), "owner/name")

	inProgress := run(1, time.Now())
	inProgress.Status = "in_progress"
	inProgress.Conclusion = ""
	c.Put(inProgress, []gh.Job{job(time.Minute)})

	if c.Has(1) {
		t.Fatal("an unfinished run must not be cached")
	}
	if c.Added() != 0 {
		t.Fatalf("nothing should have been recorded, got %d", c.Added())
	}
}

// A cache is an optimisation. Refusing to produce a report because one of its
// lines is corrupt would trade the thing this tool is for against the thing it
// is speeding up.
func TestCorruptLinesAreSkippedNotFatal(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir, "owner/name")
	c.Put(run(1, time.Now().Add(-time.Hour)), []gh.Job{job(time.Minute)})
	c.Put(run(2, time.Now().Add(-2*time.Hour)), []gh.Job{job(time.Minute)})
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}

	// A torn write: garbage in the middle and a truncated final line, which is
	// what an interrupted append actually leaves behind.
	path := Path(dir, "owner/name")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 records, got %d", len(lines))
	}
	broken := lines[0] + "\n{\"v\":1,\"run\":{\"id\":\n" + lines[1] + "\n{\"v\":1,\"ru"
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir, "owner/name")
	if err != nil {
		t.Fatalf("a corrupt cache must not be an error: %v", err)
	}
	if !reopened.Has(1) || !reopened.Has(2) {
		t.Fatal("the good records should have survived")
	}
	if reopened.Stats().Skipped == 0 {
		t.Fatal("the damage should be counted, not hidden")
	}
}

// A file with unreadable lines is rewritten rather than appended to, otherwise
// the damage is permanent and the file grows forever.
func TestFlushRewritesADamagedFile(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir, "owner/name")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json at all\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Open(dir, "owner/name")
	if err != nil {
		t.Fatal(err)
	}
	c.Put(run(1, time.Now().Add(-time.Hour)), []gh.Job{job(time.Minute)})
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "not json at all") {
		t.Fatal("the damaged line should have been rewritten away")
	}
}

func TestRecordsOlderThanMaxAgeArePruned(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir, "owner/name")
	c.Put(run(1, time.Now().Add(-time.Hour)), []gh.Job{job(time.Minute)})
	c.Put(run(2, time.Now().Add(-MaxAge-24*time.Hour)), []gh.Job{job(time.Minute)})
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}

	reopened, _ := Open(dir, "owner/name")
	if reopened.Has(2) {
		t.Fatal("a record past MaxAge should not come back")
	}
	if !reopened.Has(1) {
		t.Fatal("a recent record should")
	}
}

// The half that changes what a report can say: runs paid for by earlier
// invocations widen the window this one can cover.
func TestRunsReturnsHistoryInsideTheWindowNewestFirst(t *testing.T) {
	c, _ := Open(t.TempDir(), "owner/name")
	now := time.Now()
	c.Put(run(1, now.Add(-1*time.Hour)), nil)
	c.Put(run(2, now.Add(-48*time.Hour)), nil)
	c.Put(run(3, now.Add(-20*24*time.Hour)), nil)

	got := c.Runs(now.Add(-7 * 24 * time.Hour))
	if len(got) != 2 {
		t.Fatalf("want the two runs inside a 7-day window, got %d", len(got))
	}
	if got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("want newest first, got %d then %d", got[0].ID, got[1].ID)
	}
}

func TestPathIsContainedWithinTheCacheDirectory(t *testing.T) {
	// The repository name arrives from a command-line flag. It should not be
	// able to name a file outside the cache directory.
	dir := t.TempDir()
	for _, repo := range []string{"owner/name", "../../etc/passwd", "a/b/c"} {
		got := Path(dir, repo)
		if filepath.Dir(got) != filepath.Clean(dir) {
			t.Fatalf("%q escaped the cache directory: %s", repo, got)
		}
	}
}

func TestOpenOnAMissingFileIsTheFirstRunNotAnError(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "does", "not", "exist"), "owner/name")
	if err != nil {
		t.Fatalf("a missing cache is the first run, not a failure: %v", err)
	}
	if c.Stats().Runs != 0 {
		t.Fatal("want an empty cache")
	}
}
