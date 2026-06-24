package lsp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Manager owns the lifecycle of all configured language servers. Servers are
// started lazily on first use and reused across requests.
type Manager struct {
	workDir string
	servers []ServerConfig

	mu      sync.Mutex
	clients map[string]*Client // keyed by server name
}

// NewManager creates a Manager for the given working directory and server set.
func NewManager(workDir string, servers []ServerConfig) *Manager {
	return &Manager{
		workDir: workDir,
		servers: servers,
		clients: make(map[string]*Client),
	}
}

// Configured reports whether any language servers are configured.
func (m *Manager) Configured() bool {
	return len(m.servers) > 0
}

// Names returns the configured server names, sorted.
func (m *Manager) Names() []string {
	names := make([]string, 0, len(m.servers))
	for _, s := range m.servers {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return names
}

// Extensions returns the set of file extensions handled by any server.
func (m *Manager) Extensions() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range m.servers {
		for _, e := range s.Extensions {
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (m *Manager) configForExt(ext string) (ServerConfig, bool) {
	ext = strings.ToLower(ext)
	for _, s := range m.servers {
		for _, e := range s.Extensions {
			if strings.EqualFold(e, ext) {
				return s, true
			}
		}
	}
	return ServerConfig{}, false
}

func (m *Manager) configByName(name string) (ServerConfig, bool) {
	for _, s := range m.servers {
		if s.Name == name {
			return s, true
		}
	}
	return ServerConfig{}, false
}

// ClientForFile returns a started client capable of handling the given file,
// starting the server if necessary.
func (m *Manager) ClientForFile(ctx context.Context, path string) (*Client, error) {
	ext := filepath.Ext(path)
	cfg, ok := m.configForExt(ext)
	if !ok {
		return nil, fmt.Errorf("no language server configured for %q files", ext)
	}
	return m.getOrStart(ctx, cfg, m.rootForFile(cfg, path))
}

// ClientByName returns a started client for the named server.
func (m *Manager) ClientByName(ctx context.Context, name string) (*Client, error) {
	cfg, ok := m.configByName(name)
	if !ok {
		return nil, fmt.Errorf("no language server named %q", name)
	}
	return m.getOrStart(ctx, cfg, m.workDir)
}

func (m *Manager) getOrStart(ctx context.Context, cfg ServerConfig, root string) (*Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if c, ok := m.clients[cfg.Name]; ok {
		return c, nil
	}
	c, err := startClient(ctx, cfg, root)
	if err != nil {
		return nil, err
	}
	m.clients[cfg.Name] = c
	return c, nil
}

// Restart stops the named server (or all servers when name is empty). Servers
// restart lazily on next use. It returns the names that were restarted.
func (m *Manager) Restart(name string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if name != "" {
		if _, ok := m.configByName(name); !ok {
			return nil, fmt.Errorf("no language server named %q", name)
		}
		if c, ok := m.clients[name]; ok {
			_ = c.Shutdown()
			delete(m.clients, name)
		}
		return []string{name}, nil
	}

	var restarted []string
	for n, c := range m.clients {
		_ = c.Shutdown()
		delete(m.clients, n)
		restarted = append(restarted, n)
	}
	sort.Strings(restarted)
	return restarted, nil
}

// Running returns the names of currently-running servers.
func (m *Manager) Running() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.clients))
	for n := range m.clients {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Shutdown stops all running servers.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for n, c := range m.clients {
		_ = c.Shutdown()
		delete(m.clients, n)
	}
}

// rootForFile walks up from the file looking for one of the server's root
// markers, falling back to the manager working directory.
func (m *Manager) rootForFile(cfg ServerConfig, path string) string {
	if len(cfg.RootMarkers) == 0 {
		return m.workDir
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return m.workDir
	}
	dir := filepath.Dir(abs)
	for {
		for _, marker := range cfg.RootMarkers {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return m.workDir
}
