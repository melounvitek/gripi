package keyedlock

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSameKeySerializesWaitersAndRetainsTheirEntry(t *testing.T) {
	var mutexes Mutexes
	unlockFirst := mutexes.Lock("session")
	acquired := make(chan struct{})
	releaseSecond := make(chan struct{})
	go func() {
		unlock := mutexes.Lock("session")
		close(acquired)
		<-releaseSecond
		unlock()
	}()

	deadline := time.Now().Add(time.Second)
	for referencesFor(&mutexes, "session") != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if referencesFor(&mutexes, "session") != 2 {
		t.Fatal("waiter did not reference the held lock")
	}
	unlockFirst()
	<-acquired
	if mutexes.Len() != 1 {
		t.Fatalf("entries while waiter holds lock = %d", mutexes.Len())
	}
	close(releaseSecond)
	deadline = time.Now().Add(time.Second)
	for mutexes.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if mutexes.Len() != 0 {
		t.Fatalf("entries after final user = %d", mutexes.Len())
	}
}

func TestChurnDeletesIdleKeysWithoutBreakingSerialization(t *testing.T) {
	var mutexes Mutexes
	var active atomic.Int32
	var overlap atomic.Bool
	var group sync.WaitGroup
	for index := range 500 {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			key := fmt.Sprintf("session-%d", index%50)
			unlock := mutexes.Lock(key)
			if key == "session-0" && active.Add(1) != 1 {
				overlap.Store(true)
			}
			time.Sleep(time.Microsecond)
			if key == "session-0" {
				active.Add(-1)
			}
			unlock()
		}(index)
	}
	group.Wait()
	if overlap.Load() {
		t.Fatal("same-key critical sections overlapped")
	}
	if mutexes.Len() != 0 {
		t.Fatalf("entries after churn = %d", mutexes.Len())
	}
}

func referencesFor(mutexes *Mutexes, key string) int {
	mutexes.mu.Lock()
	defer mutexes.mu.Unlock()
	if mutexes.entries[key] == nil {
		return 0
	}
	return mutexes.entries[key].refs
}

func TestFailedTryLockReleasesItsReference(t *testing.T) {
	var mutexes Mutexes
	unlock := mutexes.Lock("session")
	if failedUnlock, ok := mutexes.TryLock("session"); ok {
		failedUnlock()
		t.Fatal("TryLock acquired held key")
	}
	if mutexes.Len() != 1 {
		t.Fatalf("entries with holder = %d", mutexes.Len())
	}
	unlock()
	if mutexes.Len() != 0 {
		t.Fatalf("entries after holder = %d", mutexes.Len())
	}
}
