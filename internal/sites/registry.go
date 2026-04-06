package sites

import (
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
