package runtime

import (
	"sort"
	"strings"
	"sync"
	"time"
)

type WhatsAppState struct {
	Status      string    `json:"status"`
	DeviceJID   string    `json:"deviceJid,omitempty"`
	LastEvent   string    `json:"lastEvent,omitempty"`
	LastError   string    `json:"lastError,omitempty"`
	QRCode      string    `json:"qrCode,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
	ConnectedAt time.Time `json:"connectedAt,omitempty"`
}

type ComponentState struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Detail    string    `json:"detail,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type State struct {
	mu         sync.RWMutex
	whatsapp   WhatsAppState
	components map[string]ComponentState
}

func New() *State {
	return &State{whatsapp: WhatsAppState{Status: "disabled", UpdatedAt: time.Now().UTC()}, components: map[string]ComponentState{}}
}

func (s *State) UpdateComponent(name, status, detail string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	detail = strings.TrimSpace(detail)
	if runes := []rune(detail); len(runes) > 300 {
		detail = string(runes[:300])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.components == nil {
		s.components = map[string]ComponentState{}
	}
	s.components[name] = ComponentState{Name: name, Status: strings.TrimSpace(status), Detail: detail, UpdatedAt: time.Now().UTC()}
}

func (s *State) Components() []ComponentState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ComponentState, 0, len(s.components))
	for _, component := range s.components {
		out = append(out, component)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *State) ComponentReadiness(expected []string) (bool, map[string]string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ready := true
	checks := make(map[string]string, len(expected))
	for _, name := range expected {
		component, ok := s.components[name]
		if !ok {
			checks[name] = "starting"
			ready = false
			continue
		}
		checks[name] = component.Status
		if component.Status != "running" {
			ready = false
		}
	}
	return ready, checks
}

func (s *State) WhatsApp() WhatsAppState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.whatsapp
}

func (s *State) UpdateWhatsApp(update func(*WhatsAppState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	update(&s.whatsapp)
	s.whatsapp.UpdatedAt = time.Now().UTC()
}
