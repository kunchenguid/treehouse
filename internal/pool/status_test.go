package pool

import (
	"testing"
	"time"

	"github.com/kunchenguid/treehouse/v3/internal/process"
)

// A slow observation must not prevent an independent caller from protecting
// its worktree with a durable lease. The listing must not overwrite that lease.
func TestListAllowsLeaseDuringProcessInspection(t *testing.T) {
	repo, poolDir := setupRepo(t)
	path, err := Acquire(repo, poolDir, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	clearOwnerReservation(t, poolDir, path)
	scanning, resume := make(chan struct{}), make(chan struct{})
	restore := swapListProcessSeams(
		func(string) ([]process.ProcessInfo, error) {
			close(scanning)
			<-resume
			return nil, nil
		},
		func(procs []process.ProcessInfo) ([]process.ProcessInfo, error) { return procs, nil },
	)
	t.Cleanup(restore)
	listed, leased := make(chan struct{}), make(chan struct{})
	var listErr, leaseErr error
	leaseStarted := false
	go func() {
		_, listErr = List(poolDir)
		close(listed)
	}()
	t.Cleanup(func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
		<-listed
		if leaseStarted {
			<-leased
		}
		if listErr != nil {
			t.Error(listErr)
		}
	})
	select {
	case <-scanning:
	case <-time.After(10 * time.Second):
		t.Fatal("listing never reaches process inspection")
	}
	leaseStarted = true
	go func() {
		_, leaseErr = LeaseExisting(poolDir, "1", "during-status")
		close(leased)
	}()
	select {
	case <-leased:
		if leaseErr != nil {
			t.Fatal(leaseErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a slow status observation blocks lease acquisition")
	}
	close(resume)
	<-listed
	if entry := entryFor(t, poolDir, path); !entry.Leased || entry.LeaseHolder != "during-status" {
		t.Fatalf("listing loses the concurrent lease: %+v", entry)
	}
}
