package runtime

import (
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

type State struct {
	mu       sync.RWMutex
	whatsapp WhatsAppState
}

func New() *State {
	return &State{whatsapp: WhatsAppState{Status: "disabled", UpdatedAt: time.Now().UTC()}}
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
