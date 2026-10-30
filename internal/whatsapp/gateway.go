package whatsapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
	"google.golang.org/protobuf/proto"
)

const maxIncidentImageBytes = 5 << 20

var errIncidentImageTooLarge = errors.New("incident image exceeds byte limit")

type boundedMediaFile struct {
	*os.File
	max int64
}

func (f *boundedMediaFile) Write(data []byte) (int, error) {
	position, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	return writeBounded(data, position, f.max, f.File.Write)
}

func (f *boundedMediaFile) WriteAt(data []byte, offset int64) (int, error) {
	return writeBounded(data, offset, f.max, func(value []byte) (int, error) {
		return f.File.WriteAt(value, offset)
	})
}

func (f *boundedMediaFile) Truncate(size int64) error {
	if size > f.max {
		return errIncidentImageTooLarge
	}
	return f.File.Truncate(size)
}

func writeBounded(data []byte, offset, limit int64, write func([]byte) (int, error)) (int, error) {
	remaining := limit - offset
	if remaining <= 0 {
		return 0, errIncidentImageTooLarge
	}
	if int64(len(data)) <= remaining {
		return write(data)
	}
	written, err := write(data[:remaining])
	if err != nil {
		return written, err
	}
	return written, errIncidentImageTooLarge
}

type inboundEnvelope struct {
	message domain.InboundMessage
	media   *waE2E.Message
}

type Gateway struct {
	core      *store.Store
	workflows *workflow.Service
	runtime   *runtimestate.State
	dsn       string
	client    *whatsmeow.Client
	container *mdbstore.Container
	messages  chan inboundEnvelope
	pairing   chan struct{}
	mu        sync.RWMutex
}

func New(core *store.Store, workflows *workflow.Service, state *runtimestate.State, dsn string) *Gateway {
	return &Gateway{core: core, workflows: workflows, runtime: state, dsn: dsn, messages: make(chan inboundEnvelope, 512), pairing: make(chan struct{}, 1)}
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
			MentionedJIDs: mentionedJIDs(evt.Message), ReplyToMessageID: quotedMessageID(evt.Message), Timestamp: evt.Info.Timestamp,
		}
		if msg.Timestamp.IsZero() {
			msg.Timestamp = time.Now().UTC()
		}
		hasImage := shouldQueueIncidentImage(msg.IsGroup, evt.Message)
		if msg.IsGroup && strings.TrimSpace(msg.Body) == "" && !hasImage {
			return
		}
		if msg.IsGroup && strings.TrimSpace(msg.Body) == "" {
			msg.Body = "Image evidence attached from WhatsApp."
		}
		envelope := inboundEnvelope{message: msg}
		if hasImage {
			envelope.media = proto.Clone(evt.Message).(*waE2E.Message)
		}
		select {
		case g.messages <- envelope:
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
		case envelope := <-g.messages:
			msg := envelope.message
			if msg.IsGroup {
				var enrich func(context.Context, *domain.InboundMessage)
				if envelope.media != nil {
					enrich = func(enrichCtx context.Context, accepted *domain.InboundMessage) {
						attachment, err := g.downloadIncidentImage(enrichCtx, accepted.MessageID, envelope.media)
						if err != nil {
							slog.Warn("discard WhatsApp incident image", "error", err, "message_id", accepted.MessageID)
							accepted.Body += " The attached image could not be retained safely."
							_ = g.core.Audit(enrichCtx, "system", "gateway", "attachment.rejected", "whatsapp_message", accepted.MessageID, "denied", map[string]any{"reason": attachmentErrorCategory(err)})
							return
						}
						accepted.Attachments = append(accepted.Attachments, attachment)
					}
				}
				incident, changed, err := g.workflows.ReceiveGroupReportWithEnricher(ctx, g.deviceJIDs(), msg, enrich)
				if err != nil {
					slog.Error("process WhatsApp report", "error", err, "message_id", msg.MessageID)
				} else if changed {
					action := "created"
					if incident.SourceMessageID != msg.MessageID {
						action = "updated"
					}
					slog.Info(action+" incident from WhatsApp report", "incident_id", incident.ID, "group_jid", msg.ChatJID, "message_id", msg.MessageID)
				}
			} else {
				contact, discovered, err := g.core.UpsertWhatsAppContact(ctx, msg)
				if err != nil {
					slog.Error("discover WhatsApp contact", "error", err, "message_id", msg.MessageID)
				}
				reply, handled := g.workflows.ReceiveOwnerCommand(ctx, msg)
				if handled && strings.TrimSpace(reply) != "" {
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

func (g *Gateway) downloadIncidentImage(ctx context.Context, messageID string, msg *waE2E.Message) (domain.IncidentAttachment, error) {
	mimeType, fileName, declaredSize, ok := incidentImageMetadata(msg)
	if !ok {
		return domain.IncidentAttachment{}, errors.New("unsupported media type")
	}
	if declaredSize <= 0 || declaredSize > maxIncidentImageBytes {
		return domain.IncidentAttachment{}, errors.New("image size exceeds limit")
	}
	g.mu.RLock()
	client := g.client
	g.mu.RUnlock()
	if client == nil {
		return domain.IncidentAttachment{}, errors.New("WhatsApp client unavailable")
	}
	downloadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	temporary, err := os.CreateTemp("", "echo-whatsapp-image-*")
	if err != nil {
		return domain.IncidentAttachment{}, fmt.Errorf("prepare image download: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
	}()
	file := &boundedMediaFile{File: temporary, max: maxIncidentImageBytes + 64}
	var downloadable whatsmeow.DownloadableMessage
	if image := msg.GetImageMessage(); image != nil {
		downloadable = image
	} else {
		downloadable = msg.GetDocumentMessage()
	}
	if downloadable == nil {
		return domain.IncidentAttachment{}, errors.New("unsupported media type")
	}
	err = client.DownloadToFile(downloadCtx, downloadable, file)
	if err != nil {
		if errors.Is(err, errIncidentImageTooLarge) {
			return domain.IncidentAttachment{}, errors.New("downloaded image size exceeds limit")
		}
		return domain.IncidentAttachment{}, fmt.Errorf("download image: %w", err)
	}
	if _, err = temporary.Seek(0, io.SeekStart); err != nil {
		return domain.IncidentAttachment{}, fmt.Errorf("read downloaded image: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(temporary, maxIncidentImageBytes+1))
	if err != nil {
		return domain.IncidentAttachment{}, fmt.Errorf("read downloaded image: %w", err)
	}
	if len(data) == 0 || len(data) > maxIncidentImageBytes {
		return domain.IncidentAttachment{}, errors.New("downloaded image size exceeds limit")
	}
	detected, err := verifiedIncidentImageMIME(mimeType, data)
	if err != nil {
		return domain.IncidentAttachment{}, err
	}
	digest := sha256.Sum256(data)
	return domain.IncidentAttachment{WhatsAppMessageID: messageID, Kind: "image", MIMEType: detected, FileName: safeAttachmentName(fileName), SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Data: data}, nil
}

func hasIncidentImageCandidate(msg *waE2E.Message) bool {
	if image := msg.GetImageMessage(); image != nil {
		return true
	}
	if document := msg.GetDocumentMessage(); document != nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(document.GetMimetype())), "image/") {
		return true
	}
	return false
}

func shouldQueueIncidentImage(isGroup bool, msg *waE2E.Message) bool {
	return isGroup && hasIncidentImageCandidate(msg)
}

func incidentImageMetadata(msg *waE2E.Message) (string, string, int64, bool) {
	if image := msg.GetImageMessage(); image != nil {
		mimeType := strings.ToLower(strings.TrimSpace(image.GetMimetype()))
		return mimeType, "", int64(image.GetFileLength()), allowedIncidentImageMIME(mimeType)
	}
	if document := msg.GetDocumentMessage(); document != nil {
		mimeType := strings.ToLower(strings.TrimSpace(document.GetMimetype()))
		return mimeType, document.GetFileName(), int64(document.GetFileLength()), allowedIncidentImageMIME(mimeType)
	}
	return "", "", 0, false
}

func allowedIncidentImageMIME(value string) bool {
	return value == "image/jpeg" || value == "image/png" || value == "image/webp"
}

func verifiedIncidentImageMIME(declared string, data []byte) (string, error) {
	detected := strings.ToLower(strings.TrimSpace(http.DetectContentType(data)))
	declared = strings.ToLower(strings.TrimSpace(declared))
	if !allowedIncidentImageMIME(detected) || declared == "" || detected != declared {
		return "", errors.New("image content type mismatch")
	}
	return detected, nil
}

func safeAttachmentName(value string) string {
	value = strings.TrimSpace(filepath.Base(value))
	value = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, value)
	return truncateAttachmentName(value, 255)
}

func truncateAttachmentName(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func attachmentErrorCategory(err error) string {
	value := strings.ToLower(err.Error())
	switch {
	case strings.Contains(value, "size"):
		return "size_limit"
	case strings.Contains(value, "type"), strings.Contains(value, "mime"):
		return "unsupported_type"
	case strings.Contains(value, "download"):
		return "download_failed"
	default:
		return "unavailable"
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
				_ = g.core.CompleteOutbound(ctx, item.ID, item.Attempts, "", parseErr)
				continue
			}
			body := item.Body
			outboundID := outboundMessageID(item.ID)
			resp, sendErr := g.client.SendMessage(ctx, jid, &waE2E.Message{Conversation: &body}, whatsmeow.SendRequestExtra{ID: outboundID})
			messageID := string(outboundID)
			if sendErr == nil && resp.ID != "" {
				messageID = string(resp.ID)
			}
			if err := g.core.CompleteOutbound(ctx, item.ID, item.Attempts, messageID, sendErr); err != nil {
				slog.Error("complete WhatsApp outbox", "error", err)
			}
		}
	}
}

func outboundMessageID(outboxID string) types.MessageID {
	digest := sha256.Sum256([]byte("echo-outbox\x00" + outboxID))
	return types.MessageID(whatsmeow.WebMessageIDPrefix + strings.ToUpper(hex.EncodeToString(digest[:9])))
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
	if contextInfo := messageContextInfo(msg); contextInfo != nil {
		return contextInfo.GetMentionedJID()
	}
	return nil
}

func quotedMessageID(msg *waE2E.Message) string {
	if contextInfo := messageContextInfo(msg); contextInfo != nil {
		return strings.TrimSpace(contextInfo.GetStanzaID())
	}
	return ""
}

func messageContextInfo(msg *waE2E.Message) *waE2E.ContextInfo {
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return ext.GetContextInfo()
	}
	if image := msg.GetImageMessage(); image != nil {
		return image.GetContextInfo()
	}
	if video := msg.GetVideoMessage(); video != nil {
		return video.GetContextInfo()
	}
	if document := msg.GetDocumentMessage(); document != nil {
		return document.GetContextInfo()
	}
	return nil
}
