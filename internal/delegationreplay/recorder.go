package delegationreplay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cosmossdk.io/log"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sys/unix"

	"github.com/valargroup/vote-sdk/ffi/zkp"
)

const (
	QueueSize          = 8
	MaxFiles           = 100
	MaxDiskBytes int64 = 10 * 1024 * 1024
	Retention          = 24 * time.Hour
	burst              = 5
)

var captures = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "svote_delegation_replay_total",
	Help: "Local delegation replay capture outcomes; labels contain no transaction identifiers.",
}, []string{"outcome"})

func init() { prometheus.MustRegister(captures) }

// Recorder has one application-owned Run goroutine. Admission never waits for
// the writer or disk. All limits are fixed to keep diagnostic configuration small.
type Recorder struct {
	dir      string
	lock     *os.File
	identity Identity
	logger   log.Logger
	queue    chan Bundle
	mu       sync.Mutex
	seen     map[string]time.Time
	tokens   float64
	last     time.Time
	stopped  bool
}

// New requires a dedicated private directory and takes an exclusive process
// lock. A reused directory must contain only files owned by this format.
// Call Run exactly once after a successful New, even when startup is cancelled.
func New(dir string, identity Identity, logger log.Logger) (*Recorder, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("capture directory must be absolute")
	}
	if !validHash(identity.ExecutableSHA256) {
		return nil, errors.New("missing executable identity")
	}
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errors.New("capture directory must be a private directory with mode 0700")
	}
	fd, err := unix.Open(filepath.Join(dir, ".lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), "capture lock")
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("capture directory is already in use")
	}
	r := &Recorder{dir: dir, lock: lock, identity: identity, logger: logger, queue: make(chan Bundle, QueueSize), seen: make(map[string]time.Time), tokens: burst, last: time.Now()}
	if _, err := r.inventory(time.Now()); err != nil {
		lock.Close()
		return nil, err
	}
	return r, nil
}

// TryCapture copies only bounded verifier evidence. Contention, queue pressure,
// and admission failures drop diagnostics without changing transaction results.
func (r *Recorder) TryCapture(proof []byte, inputs zkp.DelegationInputs, metadata Metadata, failure error) {
	if !r.mu.TryLock() {
		captures.WithLabelValues("busy").Inc()
		return
	}
	defer r.mu.Unlock()
	if r.stopped {
		captures.WithLabelValues("stopped").Inc()
		return
	}
	now := time.Now()
	r.tokens += now.Sub(r.last).Minutes()
	if r.tokens > burst {
		r.tokens = burst
	}
	r.last = now
	if r.tokens < 1 {
		captures.WithLabelValues("rate_limited").Inc()
		return
	}
	if len(r.queue) == cap(r.queue) {
		captures.WithLabelValues("queue_full").Inc()
		return
	}
	b, err := newBundle(proof, inputs, metadata, r.identity, failure, now)
	if err != nil {
		captures.WithLabelValues("invalid_or_oversize").Inc()
		return
	}
	for id, expires := range r.seen {
		if !now.Before(expires) {
			delete(r.seen, id)
		}
	}
	if _, ok := r.seen[b.ID]; ok {
		captures.WithLabelValues("duplicate").Inc()
		return
	}
	// A fixed-size cache; disk deduplication also survives restart and eviction.
	if len(r.seen) >= MaxFiles {
		var oldestID string
		var oldest time.Time
		for id, expiry := range r.seen {
			if oldestID == "" || expiry.Before(oldest) {
				oldestID, oldest = id, expiry
			}
		}
		delete(r.seen, oldestID)
	}
	select {
	case r.queue <- b:
		r.tokens--
		r.seen[b.ID] = now.Add(Retention)
		captures.WithLabelValues("queued").Inc()
	default:
		captures.WithLabelValues("queue_full").Inc()
	}
}

// Run owns writes and expiry, including periods without traffic. Shutdown drops
// pending diagnostics rather than delaying node shutdown on a queue drain.
func (r *Recorder) Run(ctx context.Context) error {
	defer func() { r.mu.Lock(); r.stopped = true; r.mu.Unlock(); r.lock.Close() }()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case b := <-r.queue:
			if err := r.write(b); err != nil {
				r.mu.Lock()
				delete(r.seen, b.ID) // Permit a later rate-limited retry after repair.
				r.mu.Unlock()
				captures.WithLabelValues("write_error").Inc()
				// Do not log bundle contents or filesystem error strings.
				r.logger.Error("delegation replay capture could not be written", "capture_id", b.ID)
			}
		case <-ticker.C:
			if _, err := r.inventory(time.Now()); err != nil {
				captures.WithLabelValues("cleanup_error").Inc()
				r.logger.Error("delegation replay retention cleanup failed")
			}
		}
	}
}

type storedFile struct {
	name     string
	size     int64
	modified time.Time
}

func (r *Recorder) inventory(now time.Time) ([]storedFile, error) {
	dir, err := os.Open(r.dir)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	// Bound directory reads even if something outside this process adds files.
	entries, err := dir.ReadDir(MaxFiles + 3)
	if err != nil {
		return nil, err
	}
	if len(entries) > MaxFiles+2 {
		return nil, errors.New("capture directory exceeds entry limit")
	}
	var files []storedFile
	for _, entry := range entries {
		name := entry.Name()
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return nil, errors.New("capture directory contains a non-private or non-regular file")
		}
		if name == ".lock" {
			continue
		}
		if name == ".pending" {
			if err := os.Remove(filepath.Join(r.dir, name)); err != nil {
				return nil, err
			}
			continue
		}
		if !strings.HasSuffix(name, ".json") || !validHash(strings.TrimSuffix(name, ".json")) {
			return nil, errors.New("capture directory contains an unknown file")
		}
		if info.Size() > MaxBundleBytes || !info.ModTime().Add(Retention).After(now) {
			if err := os.Remove(filepath.Join(r.dir, name)); err != nil {
				return nil, err
			}
			continue
		}
		files = append(files, storedFile{name, info.Size(), info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modified.Equal(files[j].modified) {
			return files[i].name < files[j].name
		}
		return files[i].modified.Before(files[j].modified)
	})
	for len(files) > MaxFiles {
		if err := os.Remove(filepath.Join(r.dir, files[0].name)); err != nil {
			return nil, err
		}
		files = files[1:]
	}
	return files, nil
}

func (r *Recorder) write(b Bundle) error {
	files, err := r.inventory(time.Now())
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.name == b.ID+".json" {
			captures.WithLabelValues("duplicate").Inc()
			return nil
		}
	}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if len(data) > MaxBundleBytes {
		return errors.New("capture too large")
	}
	var used int64
	for _, f := range files {
		used += f.size
	}
	// Make room before creating the temporary file, including its full size.
	for len(files) >= MaxFiles || used+int64(len(data)) > MaxDiskBytes {
		if len(files) == 0 {
			return errors.New("no capture storage available")
		}
		f := files[0]
		if err := os.Remove(filepath.Join(r.dir, f.name)); err != nil {
			return err
		}
		used -= f.size
		files = files[1:]
	}
	tmp := filepath.Join(r.dir, ".pending")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(r.dir, b.ID+".json")); err != nil {
		return err
	}
	captures.WithLabelValues("written").Inc()
	r.logger.Info("delegation replay capture written", "capture_id", b.ID)
	return nil
}

// OpenBundle rejects non-regular files before reading a bounded bundle.
func OpenBundle(path string) (Bundle, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return Bundle{}, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Bundle{}, err
	}
	if !info.Mode().IsRegular() {
		return Bundle{}, fmt.Errorf("replay input must be a regular file")
	}
	return Read(f)
}
