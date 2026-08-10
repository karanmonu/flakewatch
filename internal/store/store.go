// Package store keeps a local copy of the run history flakewatch has already
// paid for.
//
// The problem it exists to solve is arithmetic, not convenience. Fetching job
// data costs one API request per run, and on an active repository a few hundred
// runs is a few days: measured against gohugoio/hugo, 200 runs covered 13.5
// days and covering 30 would have needed 445 requests. Inside GitHub Actions
// the automatic token allows roughly 1,000 requests an hour for the entire
// repository, shared with every other workflow, so a month of history is not
// merely expensive there -- it is structurally out of reach.
//
// A completed workflow run never changes. Its jobs are finished, their
// durations are fixed, and the runner labels they billed against are settled.
// That immutability is what makes this safe: history fetched once is correct
// forever, so the window a report can cover stops being "whatever one
// invocation could afford" and becomes "everything ever fetched". Run daily and
// a week of genuine history accumulates for the price of the new runs each day.
//
// Everything downstream that people actually want -- monthly figures that are
// measured rather than refused, trends, and eventually "did the fix I made last
// month save anything" -- needs history that outlives a single process. This is
// that.
package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/karanmonu/flakewatch/internal/gh"
)

// formatVersion guards against reading a file written by a different layout.
//
// Bumping it makes older files unreadable, which is the correct outcome: a
// cache is a performance artefact, and silently misreading one to save a few
// hundred API requests would trade the thing this tool is for against the thing
// it is optimising.
const formatVersion = 1

// Record is one run and everything measured about it.
type Record struct {
	Version int            `json:"v"`
	Run     gh.WorkflowRun `json:"run"`
	Jobs    []gh.Job       `json:"jobs"`
	// SavedAt is when this record was written, used only for pruning. The run's
	// own timestamps are what any analysis uses.
	SavedAt time.Time `json:"saved_at"`
}

// Cache is an append-only local history for one repository.
type Cache struct {
	path    string
	records map[int64]Record
	added   []Record
	// skipped counts lines that could not be parsed. Surfaced rather than
	// swallowed, because a cache quietly dropping half its contents looks
	// exactly like a repository that got quieter.
	skipped int
}

// MaxAge is how long a record is kept.
//
// Ninety days because GitHub itself expires job data well before then, so
// anything older can never be re-fetched to check -- and a cost estimate built
// from a quarter-old runner price list is not a measurement of anything
// current.
const MaxAge = 90 * 24 * time.Hour

// Path returns the file a repository's history lives in.
//
// The repository name is flattened rather than nested so that one repo's cache
// is one file: easy to inspect, easy to delete, and impossible to leave an
// orphaned directory behind.
func Path(dir, repo string) string {
	safe := strings.NewReplacer("/", "__", string(filepath.Separator), "__", "..", "__").Replace(repo)
	return filepath.Join(dir, safe+".jsonl")
}

// DefaultDir is where history goes when the caller does not choose.
func DefaultDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locating cache directory: %w", err)
	}
	return filepath.Join(base, "flakewatch"), nil
}

// Open reads a repository's history, creating nothing until Flush.
//
// A missing file is not an error -- it is the first run. A malformed one is not
// an error either: unreadable lines are counted and skipped, because the worst
// outcome here is refusing to produce a report over a corrupted optimisation.
func Open(dir, repo string) (*Cache, error) {
	c := &Cache{path: Path(dir, repo), records: make(map[int64]Record)}

	f, err := os.Open(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening cache %s: %w", c.path, err)
	}
	defer f.Close()

	cutoff := time.Now().Add(-MaxAge)
	sc := bufio.NewScanner(f)
	// Records hold every job of a run, and a wide matrix produces a lot of them.
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil || rec.Version != formatVersion || rec.Run.ID == 0 {
			// One bad line loses one run. Aborting would lose the file. The
			// append-only format is chosen precisely so a torn final write
			// costs a single record rather than the history.
			c.skipped++
			continue
		}
		if !rec.Run.RunStartedAt.IsZero() && rec.Run.RunStartedAt.Before(cutoff) {
			continue // pruned on read; Flush makes it permanent
		}
		c.records[rec.Run.ID] = rec
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		// A truncated tail is expected after an interrupted write. Keep what
		// parsed rather than discarding a working cache over its last line.
		c.skipped++
	}
	return c, nil
}

// Jobs returns the stored jobs for a run.
func (c *Cache) Jobs(runID int64) ([]gh.Job, bool) {
	rec, ok := c.records[runID]
	if !ok {
		return nil, false
	}
	return rec.Jobs, true
}

// Has reports whether a run's jobs are already stored.
func (c *Cache) Has(runID int64) bool {
	_, ok := c.records[runID]
	return ok
}

// Put stores a run and its jobs.
//
// Only completed runs are accepted. An in-progress run's jobs are not final --
// durations are still growing and conclusions are not settled -- and caching
// one would freeze a half-measured run into history permanently, since nothing
// ever re-fetches a run it already has.
func (c *Cache) Put(run gh.WorkflowRun, jobs []gh.Job) {
	if run.ID == 0 || run.Status != "completed" {
		return
	}
	if _, exists := c.records[run.ID]; exists {
		return
	}
	rec := Record{Version: formatVersion, Run: run, Jobs: jobs, SavedAt: time.Now()}
	c.records[run.ID] = rec
	c.added = append(c.added, rec)
}

// Runs returns every stored run started at or after since, newest first.
//
// This is the half that changes what a report can say: the runs here were paid
// for by earlier invocations, so the window is the union of what this run
// fetched and everything before it.
func (c *Cache) Runs(since time.Time) []gh.WorkflowRun {
	out := make([]gh.WorkflowRun, 0, len(c.records))
	for _, rec := range c.records {
		if rec.Run.RunStartedAt.Before(since) {
			continue
		}
		out = append(out, rec.Run)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].RunStartedAt.Equal(out[j].RunStartedAt) {
			return out[i].RunStartedAt.After(out[j].RunStartedAt)
		}
		return out[i].ID > out[j].ID // stable for same-second starts
	})
	return out
}

// Stats describes what is stored.
type Stats struct {
	Runs           int
	Oldest, Newest time.Time
	Skipped        int
	Path           string
}

// Stats summarises the history, for the report to disclose.
func (c *Cache) Stats() Stats {
	s := Stats{Runs: len(c.records), Skipped: c.skipped, Path: c.path}
	for _, rec := range c.records {
		t := rec.Run.RunStartedAt
		if t.IsZero() {
			continue
		}
		if s.Oldest.IsZero() || t.Before(s.Oldest) {
			s.Oldest = t
		}
		if t.After(s.Newest) {
			s.Newest = t
		}
	}
	return s
}

// Added is how many records this process contributed.
func (c *Cache) Added() int { return len(c.added) }

// Flush writes new records to disk.
//
// Appends when nothing was pruned, which is the common path and cheap. Rewrites
// the whole file when records aged out, so pruning is real rather than a
// read-time illusion that lets the file grow without bound.
//
// The rewrite goes via a temporary file and a rename, so an interrupted flush
// leaves the previous history intact rather than a half-written one.
func (c *Cache) Flush() error {
	if len(c.added) == 0 && c.skipped == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return fmt.Errorf("creating cache directory: %w", err)
	}

	// A file with unreadable or aged-out lines gets rewritten from what is in
	// memory, which is by construction the good records only.
	if c.skipped > 0 {
		return c.rewrite()
	}

	f, err := os.OpenFile(c.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening cache for append: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	for _, rec := range c.added {
		if err := writeRecord(w, rec); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("writing cache: %w", err)
	}
	return f.Sync()
}

func (c *Cache) rewrite() error {
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".flakewatch-*")
	if err != nil {
		return fmt.Errorf("creating temporary cache: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op once the rename succeeds

	w := bufio.NewWriter(tmp)
	// Sorted so the file is stable between runs and diffable by hand.
	ids := make([]int64, 0, len(c.records))
	for id := range c.records {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if err := writeRecord(w, c.records[id]); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return fmt.Errorf("writing cache: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing cache: %w", err)
	}
	if err := os.Rename(tmp.Name(), c.path); err != nil {
		return fmt.Errorf("replacing cache: %w", err)
	}
	return nil
}

func writeRecord(w *bufio.Writer, rec Record) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encoding cache record: %w", err)
	}
	if _, err := w.Write(line); err != nil {
		return fmt.Errorf("writing cache: %w", err)
	}
	return w.WriteByte('\n')
}
