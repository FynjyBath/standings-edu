package sites

import (
	"sort"
	"strings"
	"sync"
)

type Registry struct {
	mu      sync.RWMutex
	clients map[string]SiteClient
}

func NewRegistry() *Registry {
	return &Registry{clients: make(map[string]SiteClient)}
}

func (r *Registry) Register(site string, client SiteClient) {
	site = strings.TrimSpace(strings.ToLower(site))
	if site == "" || client == nil {
		return
	}
	r.mu.Lock()
	r.clients[site] = client
	r.mu.Unlock()
}

func (r *Registry) Get(site string) (SiteClient, bool) {
	site = strings.TrimSpace(strings.ToLower(site))
	r.mu.RLock()
	c, ok := r.clients[site]
	r.mu.RUnlock()
	return c, ok
}

func (r *Registry) Sites() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.clients))
	for site := range r.clients {
		out = append(out, site)
	}
	sort.Strings(out)
	return out
}
