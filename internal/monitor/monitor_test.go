package monitor

import (
	"sync"
	"testing"
)

func TestCollectConcurrentRaceFree(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				s := Collect()
				if s.Goroutines <= 0 {
					t.Errorf("expected positive goroutine count, got %d", s.Goroutines)
				}
			}
		}()
	}
	wg.Wait()
}
