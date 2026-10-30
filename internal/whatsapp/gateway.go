package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	runtimestate "github.com/ReindeerGames/echo/internal/runtime"
	"github.com/ReindeerGames/echo/internal/store"
	"github.com/ReindeerGames/echo/internal/workflow"
	"github.com/ReindeerGames/mdbstore"
	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	waLog "github.com/polymorfa/hypermeow/util/log"
	"github.com/rs/zerolog"
)

type Gateway struct {
	core      *store.Store
	workflows *workflow.Service
	runtime   *runtimestate.State
	dsn       string
	client    *whatsmeow.Client
	container *mdbstore.Container
	messages  chan domain.InboundMessage
	pairing   chan struct{}
	mu        sync.RWMutex
}

func New(core *store.Store, workflows *workflow.Service, state *runtimestate.State, dsn string) *Gateway {
	return &Gateway{core: core, workflows: workflows, runtime: state, dsn: dsn, messages: make(chan domain.InboundMessage, 512), pairing: make(chan struct{}, 1)}
}

func (g *Gateway) RequestPairing() error {
	state := g.runtime.WhatsApp()
	if state.Status == "connected" {
		return errors.New("WhatsApp is already linked")
	}
	if state.Status == "pairing" || state.Status == "pairing_requested" || state.Status == "connecting" {
		return nil
	}
	select {
	case g.pairing <- struct{}{}:
	default:
	}
	g.runtime.UpdateWhatsApp(func(s *runtimestate.WhatsAppState) {
		s.Status = "pairing_requested"
		s.QRCode = ""
		s.LastError = ""
		s.LastEvent = "link requested"
	})
	return nil
}

func (g *Gateway) Run(ctx context.Context) error {
	release, err := g.core.AcquireNamedLock(ctx, "echo_whatsapp_primary", 2*time.Second)
	if err != nil {
		return fmt.Errorf("acquire WhatsApp ownership: %w", err)
	}
	defer release()

	g.runtime.UpdateWhatsApp(func(s *runtimestate.WhatsAppState) { s.Status = "starting"; s.LastError = "" })
	logger := zerolog.Nop()
	container, err := mdbstore.New(ctx, g.dsn, waLog.Zerolog(logger))
	if err != nil {
		return fmt.Errorf("open WhatsApp session store: %w", err)
	}
	g.container = container
	defer container.Close()
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("load WhatsApp device: %w", err)
	}
	client := whatsmeow.NewClient(device, waLog.Zerolog(logger))
	g.mu.Lock()
	g.client = client
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.client = nil
		g.mu.Unlock()
	}()
	client.AddEventHandler(g.handleEvent)

	var pairingErr <-chan error
	if device.ID == nil {
		g.runtime.UpdateWhatsApp(func(s *runtimestate.WhatsAppState) {
			s.Status = "unpaired"
			s.QRCode = ""
			s.LastError = ""
			s.LastEvent = "waiting for link request"
		})
		select {
		case <-ctx.Done():
			return nil
		case <-g.pairing:
		}
		qr, err := client.GetQRChannel(ctx)
		if err != nil {
			return fmt.Errorf("open pairing channel: %w", err)
		}
		result := make(chan error, 1)
		pairingErr = result
		go g.consumeQR(ctx, qr, result)
		g.runtime.UpdateWhatsApp(func(s *runtimestate.WhatsAppState) { s.Status = "pairing" })
	}
	if err := client.ConnectContext(ctx); err != nil {
		return fmt.Errorf("connect WhatsApp: %w", err)
	}
	defer client.Disconnect()

	workerErr := make(chan error, 2)
	go func() { workerErr <- g.consumeInbound(ctx) }()
	go func() { workerErr <- g.sendOutbox(ctx) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-pairingErr:
		return err
	case err := <-workerErr:
		return err
	}
}

func (g *Gateway) ListGroups(ctx context.Context) ([]domain.WhatsAppGroup, error) {
	g.mu.RLock()
	client := g.client
	g.mu.RUnlock()
	if client == nil || !client.IsConnected() {
		return nil, errors.New("WhatsApp is not connected")
	}
	groups, err := client.GetJoinedGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("list joined WhatsApp groups: %w", err)
	}
	out := make([]domain.WhatsAppGroup, 0, len(groups))
	for _, group := range groups {
		if group == nil {
			continue
		}
		out = append(out, domain.WhatsAppGroup{JID: group.JID.String(), Name: group.Name, ParticipantCount: group.ParticipantCount, Suspended: group.Suspended})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

func (g *Gateway) consumeQR(ctx context.Context, qr <-chan whatsmeow.QRChannelItem, result chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case item, ok := <-qr:
			if !ok {
				g.pairingFailed("pairing ended before the device was linked")
				result <- errors.New("WhatsApp pairing ended before the device was linked")
				return
			}
			g.runtime.UpdateWhatsApp(func(s *runtimestate.WhatsAppState) {
				s.LastEvent = item.Event
				if item.Event == whatsmeow.QRChannelEventCode {
					s.QRCode = item.Code
					s.Status = "pairing"
				}
				if item.Event == "success" {
					s.QRCode = ""
					if s.Status != "connected" {
						s.Status = "connecting"
					}
				}
			})
			if item.Event == "success" {
				return
			}
			if item.Error != nil || item.Event == whatsmeow.QRChannelEventError {
				err := item.Error
				if err == nil {
					err = errors.New("WhatsApp rejected the pairing request")
				}
				g.pairingFailed(err.Error())
				result <- err
				return
			}
			if item.Event == "timeout" {
				err := errors.New("WhatsApp pairing timed out; choose Link device to try again")
				g.pairingFailed(err.Error())
				result <- err
				return
			}
		}
	}
}

func (g *Gateway) pairingFailed(message string) {
	g.runtime.UpdateWhatsApp(func(s *runtimestate.WhatsAppState) {
		s.Status = "unpaired"
		s.QRCode = ""
		s.LastError = message
	})
}

func (g *Gateway) handleEvent(raw any) {
	switch evt := raw.(type) {
	case *events.Connected:
		jid := g.deviceJID()
		g.runtime.UpdateWhatsApp(func(s *runtimestate.WhatsAppState) {
			s.Status = "connected"
			s.DeviceJID = jid
			s.QRCode = ""
			s.LastError = ""
			s.ConnectedAt = time.Now().UTC()
			s.LastEvent = "connected"
		})
	case *events.Disconnected:
		g.runtime.UpdateWhatsApp(func(s *runtimestate.WhatsAppState) { s.Status = "disconnected"; s.LastEvent = "disconnected" })
	case *events.LoggedOut:
		g.runtime.UpdateWhatsApp(func(s *runtimestate.WhatsAppState) {
			s.Status = "logged_out"
			s.LastEvent = "logged_out"
			s.LastError = fmt.Sprint(evt.Reason)
		})
	case *events.StreamReplaced:
		g.runtime.UpdateWhatsApp(func(s *runtimestate.WhatsAppState) {
			s.Status = "stream_replaced"
			s.LastEvent = "stream_replaced"
			s.LastError = "another client took ownership of this session"
		})
	case *events.Message:
		if evt.Info.IsFromMe || evt.Message == nil {
			return
		}
		msg := domain.InboundMessage{
			MessageID: string(evt.Info.ID), ChatJID: evt.Info.Chat.String(), SenderJID: evt.Info.Sender.String(),
			SenderAltJID: evt.Info.SenderAlt.String(), SenderName: evt.Info.PushName, Body: messageBody(evt.Message), IsGroup: evt.Info.IsGroup,
			MentionedJIDs: mentionedJIDs(evt.Message), Timestamp: evt.Info.Timestamp,
		}
		if msg.Timestamp.IsZero() {
			msg.Timestamp = time.Now().UTC()
		}
		if msg.IsGroup && strings.TrimSpace(msg.Body) == "" {
			return
		}
		select {
		case g.messages <- msg:
		default:
			slog.Error("WhatsApp inbound queue full", "message_id", msg.MessageID)
			_ = g.core.Audit(context.Background(), "system", "gateway", "message.dropped", "whatsapp_message", msg.MessageID, "failed", map[string]any{"reason": "inbound queue full"})
		}
	}
}

func (g *Gateway) consumeInbound(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg := <-g.messages:
			if msg.IsGroup {
				incident, created, err := g.workflows.ReceiveGroupReport(ctx, g.deviceJIDs(), msg)
				if err != nil {
					slog.Error("process WhatsApp report", "error", err, "message_id", msg.MessageID)
				} else if created {
					slog.Info("created incident from WhatsApp report", "incident_id", incident.ID, "group_jid", msg.ChatJID, "message_id", msg.MessageID)
				}
			} else {
				contact, discovered, err := g.core.UpsertWhatsAppContact(ctx, msg)
				if err != nil {
					slog.Error("discover WhatsApp contact", "error", err, "message_id", msg.MessageID)
				}
				reply, handled := g.workflows.ReceiveOwnerCommand(ctx, msg)
				if handled {
					if err := g.core.EnqueueOutbound(ctx, msg.ChatJID, reply); err != nil {
						slog.Error("enqueue owner command reply", "error", err)
					}
				} else if discovered {
					_ = g.core.Audit(ctx, "whatsapp", contact.JID, "contact.discovered", "whatsapp_contact", contact.ID, "succeeded", map[string]any{"displayName": contact.DisplayName})
					body := "Thanks — Echo has discovered this contact. An authenticated administrator can now select you as the owner in Echo’s WhatsApp settings."
					if err := g.core.EnqueueOutbound(ctx, msg.ChatJID, body); err != nil {
						slog.Error("enqueue contact discovery reply", "error", err)
					}
				}
			}
		}
	}
}

func (g *Gateway) sendOutbox(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if !g.connected() {
				continue
			}
			item, err := g.core.ClaimOutbound(ctx, 90*time.Second)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				slog.Error("claim WhatsApp outbox", "error", err)
				continue
			}
			jid, parseErr := types.ParseJID(item.ChatJID)
			if parseErr != nil {
				_ = g.core.CompleteOutbound(ctx, item.ID, "", parseErr)
				continue
			}
			body := item.Body
			resp, sendErr := g.client.SendMessage(ctx, jid, &waE2E.Message{Conversation: &body})
			messageID := ""
			if sendErr == nil {
				messageID = string(resp.ID)
			}
			if err := g.core.CompleteOutbound(ctx, item.ID, messageID, sendErr); err != nil {
				slog.Error("complete WhatsApp outbox", "error", err)
			}
		}
	}
}

func (g *Gateway) connected() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.client != nil && g.client.IsConnected()
}
func (g *Gateway) deviceJID() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.client == nil || g.client.Store == nil || g.client.Store.ID == nil {
		return ""
	}
	return g.client.Store.ID.String()
}
func (g *Gateway) deviceJIDs() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.client == nil || g.client.Store == nil {
		return nil
	}
	out := make([]string, 0, 2)
	if g.client.Store.ID != nil {
		out = append(out, g.client.Store.ID.ToNonAD().String())
	}
	if !g.client.Store.LID.IsEmpty() {
		out = append(out, g.client.Store.LID.ToNonAD().String())
	}
	return out
}

func messageBody(msg *waE2E.Message) string {
	if text := strings.TrimSpace(msg.GetConversation()); text != "" {
		return text
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return strings.TrimSpace(ext.GetText())
	}
	if image := msg.GetImageMessage(); image != nil {
		return strings.TrimSpace(image.GetCaption())
	}
	if video := msg.GetVideoMessage(); video != nil {
		return strings.TrimSpace(video.GetCaption())
	}
	if document := msg.GetDocumentMessage(); document != nil {
		return strings.TrimSpace(document.GetCaption())
	}
	return ""
}

func mentionedJIDs(msg *waE2E.Message) []string {
	if ext := msg.GetExtendedTextMessage(); ext != nil && ext.GetContextInfo() != nil {
		return ext.GetContextInfo().GetMentionedJID()
	}
	if image := msg.GetImageMessage(); image != nil && image.GetContextInfo() != nil {
		return image.GetContextInfo().GetMentionedJID()
	}
	if video := msg.GetVideoMessage(); video != nil && video.GetContextInfo() != nil {
		return video.GetContextInfo().GetMentionedJID()
	}
	if document := msg.GetDocumentMessage(); document != nil && document.GetContextInfo() != nil {
		return document.GetContextInfo().GetMentionedJID()
	}
	return nil
}
