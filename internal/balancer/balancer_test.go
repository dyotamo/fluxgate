package balancer

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRoundRobinSequentialEqualDistributionAndSingleBackend(t *testing.T) {
	backends := makeBackends(t, 1, 1, 1)
	balancer := newTestBalancer(t, RoundRobin, backends...)
	counts := make(map[string]int)
	for index := 0; index < 12; index++ {
		selection := next(t, balancer)
		counts[selection.Backend.ID]++
		selection.Release()
	}
	for _, backend := range backends {
		if counts[backend.ID] != 4 {
			t.Errorf("backend %s selected %d times, want 4", backend.ID, counts[backend.ID])
		}
	}

	single := newTestBalancer(t, RoundRobin, makeBackends(t, 1)[0])
	for index := 0; index < 10; index++ {
		selection := next(t, single)
		if selection.Backend.ID != "backend-1" {
			t.Fatalf("single backend selection = %q", selection.Backend.ID)
		}
		selection.Release()
	}
}

func TestRoundRobinConcurrentSelectionIsSafeAndEven(t *testing.T) {
	backends := makeBackends(t, 1, 1, 1, 1)
	balancer := newTestBalancer(t, RoundRobin, backends...)
	counts := make([]atomic.Int64, len(backends))
	const workers = 16
	const requestsPerWorker = 1000

	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for request := 0; request < requestsPerWorker; request++ {
				selection, err := balancer.Next()
				if err != nil {
					t.Errorf("Next() error = %v", err)
					return
				}
				index := int(selection.Backend.ID[len("backend-")] - '1')
				counts[index].Add(1)
				selection.Release()
			}
		}()
	}
	wait.Wait()

	for index := range counts {
		if got := counts[index].Load(); got != int64(workers*requestsPerWorker/len(counts)) {
			t.Errorf("backend %d selected %d times, want equal distribution", index+1, got)
		}
	}
}

func TestRoundRobinSkipsAndRecoversIneligibleBackends(t *testing.T) {
	backends := makeBackends(t, 1, 1)
	balancer := newTestBalancer(t, RoundRobin, backends...)
	backends[0].SetEligible(false)
	selection := next(t, balancer)
	if selection.Backend != backends[1] {
		t.Fatalf("selected %q while backend-1 was ineligible", selection.Backend.ID)
	}
	selection.Release()
	backends[0].SetEligible(true)
	selection = next(t, balancer)
	if selection.Backend != backends[0] {
		t.Fatalf("selected %q after backend-1 became eligible", selection.Backend.ID)
	}
	selection.Release()
}

func TestRandomSelectsEligibleBackendsAndRespectsWeights(t *testing.T) {
	backends := makeBackends(t, 1, 1, 1)
	balancer := newTestBalancer(t, Random, backends...)
	selected := make(map[string]bool)
	for index := 0; index < 600; index++ {
		selection := next(t, balancer)
		selected[selection.Backend.ID] = true
		selection.Release()
	}
	if len(selected) != len(backends) {
		t.Fatalf("random selected %d backends, want all %d", len(selected), len(backends))
	}

	weightedBackends := makeBackends(t, 3, 1)
	weighted := newTestBalancer(t, Random, weightedBackends...)
	counts := make(map[string]int)
	const sampleSize = 20000
	for index := 0; index < sampleSize; index++ {
		selection := next(t, weighted)
		counts[selection.Backend.ID]++
		selection.Release()
	}
	ratio := float64(counts["backend-1"]) / sampleSize
	if ratio < 0.72 || ratio > 0.78 {
		t.Fatalf("weighted random first backend ratio = %.3f, want about 0.75", ratio)
	}
}

func TestLeastConnectionsSelectionTiesAndCounters(t *testing.T) {
	backends := makeBackends(t, 1, 1)
	balancer := newTestBalancer(t, LeastConnections, backends...)
	first := next(t, balancer)
	second := next(t, balancer)
	third := next(t, balancer)
	if first.Backend != backends[0] || second.Backend != backends[1] || third.Backend != backends[0] {
		t.Fatalf("least-connections selections = %q, %q, %q; want backend-1, backend-2, backend-1", first.Backend.ID, second.Backend.ID, third.Backend.ID)
	}
	if backends[0].ActiveRequests() != 2 || backends[1].ActiveRequests() != 1 {
		t.Fatalf("active requests = %d, %d; want 2, 1", backends[0].ActiveRequests(), backends[1].ActiveRequests())
	}
	first.Release()
	first.Release()
	second.Release()
	third.Release()
	if backends[0].ActiveRequests() != 0 || backends[1].ActiveRequests() != 0 {
		t.Fatalf("active counters after release = %d, %d; want 0, 0", backends[0].ActiveRequests(), backends[1].ActiveRequests())
	}
}

func TestLeastConnectionsReturnsNoEligibleBackend(t *testing.T) {
	backends := makeBackends(t, 1, 1)
	balancer := newTestBalancer(t, LeastConnections, backends...)
	for _, backend := range backends {
		backend.SetEligible(false)
	}
	if _, err := balancer.Next(); !errors.Is(err, ErrNoEligibleBackend) {
		t.Fatalf("Next() error = %v, want ErrNoEligibleBackend", err)
	}
}

func TestWeightedRoundRobinDistribution(t *testing.T) {
	backends := makeBackends(t, 5, 2, 1)
	balancer := newTestBalancer(t, WeightedRoundRobin, backends...)
	counts := make(map[string]int)
	for index := 0; index < 8; index++ {
		selection := next(t, balancer)
		counts[selection.Backend.ID]++
		selection.Release()
	}
	for index, weight := range []int{5, 2, 1} {
		id := fmt.Sprintf("backend-%d", index+1)
		if counts[id] != weight {
			t.Errorf("backend %s selected %d times, want %d", id, counts[id], weight)
		}
	}
}

func TestWeightedRoundRobinEqualWeightsAndLargeWeights(t *testing.T) {
	equalBackends := makeBackends(t, 1, 1, 1)
	equal := newTestBalancer(t, WeightedRoundRobin, equalBackends...)
	for index := range equalBackends {
		selection := next(t, equal)
		if selection.Backend != equalBackends[index] {
			t.Fatalf("equal-weight selection %d = %q, want %q", index, selection.Backend.ID, equalBackends[index].ID)
		}
		selection.Release()
	}

	largeBackends := makeBackends(t, 1000000, 1)
	large := newTestBalancer(t, WeightedRoundRobin, largeBackends...)
	for index := 0; index < 1001; index++ {
		selection := next(t, large)
		selection.Release()
	}
	if largeBackends[0].ActiveRequests() != 0 || largeBackends[1].ActiveRequests() != 0 {
		t.Fatal("large-weight selections leaked active request counts")
	}
}

func TestAlgorithmsRejectInvalidPools(t *testing.T) {
	if _, err := New("unknown", makeBackends(t, 1)); err == nil {
		t.Fatal("New() accepted an unknown algorithm")
	}
	if _, err := New(RoundRobin, nil); err == nil {
		t.Fatal("New() accepted an empty pool")
	}
	if _, err := NewBackend("backend", "http://localhost:3000", 0); err == nil {
		t.Fatal("NewBackend() accepted a zero weight")
	}
}

func makeBackends(t *testing.T, weights ...int) []*Backend {
	t.Helper()
	backends := make([]*Backend, 0, len(weights))
	for index, weight := range weights {
		backend, err := NewBackend(fmt.Sprintf("backend-%d", index+1), fmt.Sprintf("http://localhost:%d", 3000+index), weight)
		if err != nil {
			t.Fatalf("NewBackend() error = %v", err)
		}
		backends = append(backends, backend)
	}
	return backends
}

func newTestBalancer(t *testing.T, algorithm Algorithm, backends ...*Backend) Balancer {
	t.Helper()
	balancer, err := New(algorithm, backends)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return balancer
}

func next(t *testing.T, balancer Balancer) *Lease {
	t.Helper()
	selection, err := balancer.Next()
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	return selection
}
