package balancer

import (
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Algorithm string

const (
	RoundRobin         Algorithm = "round_robin"
	LeastConnections   Algorithm = "least_connections"
	Random             Algorithm = "random"
	WeightedRoundRobin Algorithm = "weighted_round_robin"
)

var ErrNoEligibleBackend = errors.New("no eligible backend")

type Backend struct {
	ID     string
	URL    string
	Weight int

	activeRequests atomic.Int64
	eligible       atomic.Bool
}

type Lease struct {
	Backend *Backend
	once    sync.Once
}

type Balancer interface {
	Next() (*Lease, error)
}

func ParseAlgorithm(value string) (Algorithm, error) {
	algorithm := Algorithm(strings.ToLower(strings.TrimSpace(value)))
	if algorithm == "" {
		return RoundRobin, nil
	}
	switch algorithm {
	case RoundRobin, LeastConnections, Random, WeightedRoundRobin:
		return algorithm, nil
	default:
		return "", fmt.Errorf("unsupported load balancing algorithm %q", value)
	}
}

func NewBackend(id, rawURL string, weight int) (*Backend, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("backend ID is required")
	}
	if weight <= 0 {
		return nil, fmt.Errorf("backend weight must be greater than zero")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("backend URL must be an absolute http or https URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("backend URL must not contain credentials, a query, or a fragment")
	}
	backend := &Backend{ID: id, URL: rawURL, Weight: weight}
	backend.eligible.Store(true)
	return backend, nil
}

func New(algorithm Algorithm, backends []*Backend) (Balancer, error) {
	parsed, err := ParseAlgorithm(string(algorithm))
	if err != nil {
		return nil, err
	}
	if len(backends) == 0 {
		return nil, fmt.Errorf("backend pool must not be empty")
	}

	pool := append([]*Backend(nil), backends...)
	ids := make(map[string]bool, len(pool))
	var totalWeight int64
	for _, backend := range pool {
		if backend == nil {
			return nil, fmt.Errorf("backend pool contains a nil backend")
		}
		if backend.ID == "" || ids[backend.ID] {
			return nil, fmt.Errorf("backend IDs must be non-empty and unique")
		}
		ids[backend.ID] = true
		if backend.Weight <= 0 {
			return nil, fmt.Errorf("backend %q weight must be greater than zero", backend.ID)
		}
		weight := int64(backend.Weight)
		if weight > int64(^uint64(0)>>1)-totalWeight {
			return nil, fmt.Errorf("sum of backend weights overflows")
		}
		totalWeight += weight
	}

	switch parsed {
	case RoundRobin:
		return &roundRobinBalancer{backends: pool}, nil
	case LeastConnections:
		return &leastConnectionsBalancer{backends: pool}, nil
	case Random:
		return &randomBalancer{
			backends: pool,
			random:   rand.New(rand.NewSource(time.Now().UnixNano())),
		}, nil
	case WeightedRoundRobin:
		return &weightedRoundRobinBalancer{
			backends:       pool,
			currentWeights: make([]int64, len(pool)),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported load balancing algorithm %q", parsed)
	}
}

func (b *Backend) ActiveRequests() int64 {
	return b.activeRequests.Load()
}

func (b *Backend) Eligible() bool {
	return b.eligible.Load()
}

func (b *Backend) SetEligible(eligible bool) {
	b.eligible.Store(eligible)
}

func (l *Lease) Release() {
	if l == nil || l.Backend == nil {
		return
	}
	l.once.Do(func() {
		l.Backend.release()
	})
}

func (b *Backend) acquire() bool {
	if !b.eligible.Load() {
		return false
	}
	b.activeRequests.Add(1)
	if !b.eligible.Load() {
		b.release()
		return false
	}
	return true
}

func (b *Backend) release() {
	for {
		active := b.activeRequests.Load()
		if active <= 0 || b.activeRequests.CompareAndSwap(active, active-1) {
			return
		}
	}
}

func lease(backend *Backend) (*Lease, bool) {
	if !backend.acquire() {
		return nil, false
	}
	return &Lease{Backend: backend}, true
}

type roundRobinBalancer struct {
	backends []*Backend
	next     int
	mu       sync.Mutex
}

func (b *roundRobinBalancer) Next() (*Lease, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for attempts := 0; attempts < len(b.backends); attempts++ {
		index := b.next
		b.next = (b.next + 1) % len(b.backends)
		if selected, ok := lease(b.backends[index]); ok {
			return selected, nil
		}
	}
	return nil, ErrNoEligibleBackend
}

type leastConnectionsBalancer struct {
	backends []*Backend
	mu       sync.Mutex
}

func (b *leastConnectionsBalancer) Next() (*Lease, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for attempts := 0; attempts < len(b.backends); attempts++ {
		selectedIndex := -1
		var selectedRequests int64
		for index, backend := range b.backends {
			if !backend.Eligible() {
				continue
			}
			active := backend.ActiveRequests()
			if selectedIndex == -1 || active < selectedRequests {
				selectedIndex = index
				selectedRequests = active
			}
		}
		if selectedIndex == -1 {
			return nil, ErrNoEligibleBackend
		}
		if selected, ok := lease(b.backends[selectedIndex]); ok {
			return selected, nil
		}
	}
	return nil, ErrNoEligibleBackend
}

type randomBalancer struct {
	backends []*Backend
	random   *rand.Rand
	mu       sync.Mutex
}

func (b *randomBalancer) Next() (*Lease, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	candidates := append([]*Backend(nil), b.backends...)
	for len(candidates) > 0 {
		var totalWeight int64
		for _, backend := range candidates {
			if backend.Eligible() {
				totalWeight += int64(backend.Weight)
			}
		}
		if totalWeight == 0 {
			return nil, ErrNoEligibleBackend
		}

		selection := b.random.Int63n(totalWeight)
		var selected *Backend
		for _, backend := range candidates {
			if !backend.Eligible() {
				continue
			}
			weight := int64(backend.Weight)
			if selection < weight {
				selected = backend
				break
			}
			selection -= weight
		}
		if selected == nil {
			return nil, ErrNoEligibleBackend
		}
		if result, ok := lease(selected); ok {
			return result, nil
		}
		candidates = removeBackend(candidates, selected)
	}
	return nil, ErrNoEligibleBackend
}

type weightedRoundRobinBalancer struct {
	backends       []*Backend
	currentWeights []int64
	mu             sync.Mutex
}

func (b *weightedRoundRobinBalancer) Next() (*Lease, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for attempts := 0; attempts < len(b.backends); attempts++ {
		selectedIndex := -1
		var totalWeight int64
		for index, backend := range b.backends {
			if !backend.Eligible() {
				b.currentWeights[index] = 0
				continue
			}
			weight := int64(backend.Weight)
			totalWeight += weight
			b.currentWeights[index] += weight
			if selectedIndex == -1 || b.currentWeights[index] > b.currentWeights[selectedIndex] {
				selectedIndex = index
			}
		}
		if selectedIndex == -1 {
			return nil, ErrNoEligibleBackend
		}
		b.currentWeights[selectedIndex] -= totalWeight
		if selected, ok := lease(b.backends[selectedIndex]); ok {
			return selected, nil
		}
	}
	return nil, ErrNoEligibleBackend
}

func removeBackend(backends []*Backend, target *Backend) []*Backend {
	for index, backend := range backends {
		if backend == target {
			return append(backends[:index], backends[index+1:]...)
		}
	}
	return backends
}
