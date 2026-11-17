package delegationreplay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cosmossdk.io/log"
	"github.com/stretchr/testify/require"
	"github.com/valargroup/vote-sdk/ffi/zkp"
)

func testIdentity() Identity { return Identity{ExecutableSHA256: strings.Repeat("a", 64)} }
func testInputs() zkp.DelegationInputs {
	b := bytes.Repeat([]byte{1}, 32)
	return zkp.DelegationInputs{Rk: bytes.Clone(b), SignedNoteNullifier: bytes.Clone(b), CmxNew: bytes.Clone(b), VanCmx: bytes.Clone(b), VoteRoundId: bytes.Clone(b), NcRoot: bytes.Clone(b), NullifierImtRoot: bytes.Clone(b), GovNullifiers: [][]byte{bytes.Clone(b)}}
}
func testBundle(t *testing.T) Bundle {
	t.Helper()
	b, err := newBundle([]byte{1, 2, 3}, testInputs(), Metadata{Phase: "check_tx"}, testIdentity(), errors.New("invalid proof"), time.Now())
	require.NoError(t, err)
	return b
}
func testRecorder(t *testing.T) *Recorder {
	t.Helper()
	r, err := New(filepath.Join(t.TempDir(), "captures"), testIdentity(), log.NewNopLogger())
	require.NoError(t, err)
	t.Cleanup(func() { r.lock.Close() })
	return r
}

type failingVerifier struct {
	zkp.MockVerifier
	err   error
	calls int
}

func (v *failingVerifier) VerifyDelegation([]byte, zkp.DelegationInputs) error {
	v.calls++
	return v.err
}

func TestReplayClassifiesResultsAndRequiresIdentity(t *testing.T) {
	b := testBundle(t)
	for _, tc := range []struct {
		name    string
		failure error
		want    string
	}{
		{"same rejection", errors.New(b.Error), "rejection reproduced"},
		{"different rejection", errors.New("different"), "rejected with a different error"},
		{"unexpected acceptance", nil, "verified unexpectedly"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := &failingVerifier{err: tc.failure}
			result, err := Replay(b, testIdentity(), v, false)
			require.NoError(t, err)
			require.Equal(t, tc.want, result)
		})
	}
	v := &failingVerifier{err: errors.New(b.Error)}
	different := Identity{ExecutableSHA256: strings.Repeat("b", 64)}
	_, err := Replay(b, different, v, false)
	require.ErrorContains(t, err, "executable differs")
	require.Zero(t, v.calls)
	result, err := Replay(b, different, v, true)
	require.NoError(t, err)
	require.Equal(t, "comparison: rejection reproduced", result)
}

func TestReadRejectsTamperingExtraDataAndOversize(t *testing.T) {
	b := testBundle(t)
	data, err := json.Marshal(b)
	require.NoError(t, err)
	decoded, err := Read(bytes.NewReader(data))
	require.NoError(t, err)
	require.Equal(t, b.ID, decoded.ID)
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"oversize", bytes.Repeat([]byte{' '}, MaxBundleBytes+1)},
		{"extra object", append(bytes.Clone(data), []byte("{}")...)},
		{"truncated", data[:len(data)-1]},
		{"changed proof", bytes.Replace(data, []byte(`"proof":"AQID"`), []byte(`"proof":"AQIE"`), 1)},
		{"unknown field", append([]byte(`{"private_witness":"no",`), data[1:]...)},
	} {
		t.Run(tc.name, func(t *testing.T) { _, err := Read(bytes.NewReader(tc.data)); require.Error(t, err) })
	}
}

func TestCaptureOwnsBytesAndDeduplicatesWithoutTransactionHash(t *testing.T) {
	r := testRecorder(t)
	inputs := testInputs()
	proof := []byte{1, 2, 3}
	r.TryCapture(proof, inputs, Metadata{TxHash: strings.Repeat("a", 64)}, errors.New("invalid"))
	r.TryCapture(proof, inputs, Metadata{TxHash: strings.Repeat("b", 64)}, errors.New("invalid"))
	require.Len(t, r.queue, 1)
	proof[0] = 9
	inputs.NcRoot[0] = 9
	inputs.GovNullifiers[0][0] = 9
	b := <-r.queue
	require.Equal(t, byte(1), b.Proof[0])
	require.Equal(t, byte(1), b.Inputs.NcRoot[0])
	require.Equal(t, byte(1), b.Inputs.GovNullifiers[0][0])
	require.NoError(t, b.Validate())
	r.TryCapture(proof, inputs, Metadata{}, errors.New("invalid"))
	require.Len(t, r.queue, 1)
}

func TestAdmissionLimitsAndContention(t *testing.T) {
	r := testRecorder(t)
	for i := 0; i < 20; i++ {
		r.TryCapture([]byte{byte(i)}, testInputs(), Metadata{}, errors.New("invalid"))
	}
	require.Len(t, r.queue, burst)
	r.mu.Lock()
	done := make(chan struct{})
	go func() { r.TryCapture([]byte{99}, testInputs(), Metadata{}, errors.New("invalid")); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("capture waited for admission lock")
	}
	r.mu.Unlock()
	// Saturate the queue independently of the rate limiter.
	for len(r.queue) < cap(r.queue) {
		r.queue <- testBundle(t)
	}
	r.tokens = burst
	r.TryCapture([]byte{99}, testInputs(), Metadata{}, errors.New("invalid"))
	require.Len(t, r.queue, QueueSize)
	for len(r.queue) > 0 {
		<-r.queue
	}
	r.TryCapture(make([]byte, MaxBundleBytes), testInputs(), Metadata{}, errors.New("invalid"))
	require.Empty(t, r.queue)
}

func TestStorageBoundsPrivatePermissionsAndRestartDeduplication(t *testing.T) {
	r := testRecorder(t)
	b := testBundle(t)
	for i := 0; i < MaxFiles+5; i++ {
		b.Proof = []byte{byte(i)}
		b.ID = b.captureID()
		b.Integrity = b.checksum()
		require.NoError(t, r.write(b))
	}
	files, err := r.inventory(time.Now())
	require.NoError(t, err)
	require.Len(t, files, MaxFiles)
	var size int64
	for _, file := range files {
		info, err := os.Stat(filepath.Join(r.dir, file.name))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())
		size += info.Size()
	}
	require.LessOrEqual(t, size, MaxDiskBytes)
	r.lock.Close()
	r2, err := New(r.dir, testIdentity(), log.NewNopLogger())
	require.NoError(t, err)
	defer r2.lock.Close()
	require.NoError(t, r2.write(b))
	files, err = r2.inventory(time.Now())
	require.NoError(t, err)
	require.Len(t, files, MaxFiles)
	loaded, err := OpenBundle(filepath.Join(r.dir, b.ID+".json"))
	require.NoError(t, err)
	require.Equal(t, b, loaded)
	// Cleanup also runs without new captures, using the same inventory path.
	files, err = r2.inventory(time.Now().Add(Retention + time.Minute))
	require.NoError(t, err)
	require.Empty(t, files)
}

func TestDirectoryGuardsAndAtomicWriteFailure(t *testing.T) {
	r := testRecorder(t)
	_, err := New(r.dir, testIdentity(), log.NewNopLogger())
	require.ErrorContains(t, err, "already in use")
	outside := filepath.Join(t.TempDir(), "untouched")
	require.NoError(t, os.WriteFile(outside, []byte("untouched"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(r.dir, strings.Repeat("f", 64)+".json")))
	require.Error(t, r.write(testBundle(t)))
	data, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "untouched", string(data))
	_, err = OpenBundle(filepath.Join(r.dir, strings.Repeat("f", 64)+".json"))
	require.Error(t, err)
	public := filepath.Join(t.TempDir(), "public")
	require.NoError(t, os.Mkdir(public, 0755))
	_, err = New(public, testIdentity(), log.NewNopLogger())
	require.Error(t, err)
}

func TestRunOwnsShutdownAndConcurrentAdmission(t *testing.T) {
	r := testRecorder(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.TryCapture([]byte{byte(i)}, testInputs(), Metadata{}, fmt.Errorf("invalid"))
		}(i)
	}
	wg.Wait()
	cancel()
	require.NoError(t, <-done)
	r.TryCapture([]byte{99}, testInputs(), Metadata{}, errors.New("invalid"))
	r.mu.Lock()
	require.True(t, r.stopped)
	r.mu.Unlock()
	r2, err := New(r.dir, testIdentity(), log.NewNopLogger())
	require.NoError(t, err)
	r2.lock.Close()
}
