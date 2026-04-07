package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vietbui/chat-quality-agent/channels"
	"github.com/vietbui/chat-quality-agent/config"
	"github.com/vietbui/chat-quality-agent/db"
	"github.com/vietbui/chat-quality-agent/db/models"
	"github.com/vietbui/chat-quality-agent/pkg"
)

// SyncEngine handles pulling messages from external channels into the database.
type SyncEngine struct {
	cfg *config.Config
}

func NewSyncEngine(cfg *config.Config) *SyncEngine {
	return &SyncEngine{cfg: cfg}
}

// SyncChannel syncs a single channel: fetches conversations + messages and upserts into DB.
func (s *SyncEngine) SyncChannel(ctx context.Context, channel models.Channel) error {
	log.Printf("[sync] starting sync for channel %s (%s)", channel.Name, channel.ChannelType)

	// Decrypt credentials
	credBytes, err := pkg.Decrypt(channel.CredentialsEncrypted, s.cfg.EncryptionKey)
	if err != nil {
		return s.updateSyncStatus(channel.ID, "error", fmt.Sprintf("decrypt failed: %v", err))
	}

	adapter, err := channels.NewAdapter(channel.ChannelType, credBytes)
	if err != nil {
		return s.updateSyncStatus(channel.ID, "error", fmt.Sprintf("adapter init failed: %v", err))
	}

	// Set token refresh callback for Zalo — persist new tokens to DB after refresh
	if zaloAdapter, ok := adapter.(*channels.ZaloOAAdapter); ok {
		chID := channel.ID
		encKey := s.cfg.EncryptionKey
		zaloAdapter.SetTokenRefreshCallback(func(newAccess, newRefresh string) {
			var ch models.Channel
			if db.DB.First(&ch, "id = ?", chID).Error != nil {
				return
			}
			oldCreds, err := pkg.Decrypt(ch.CredentialsEncrypted, encKey)
			if err != nil {
				log.Printf("[sync] decrypt failed for token persist: %v", err)
				return
			}
			var credsMap map[string]interface{}
			if err := json.Unmarshal(oldCreds, &credsMap); err != nil {
				log.Printf("[sync] unmarshal creds failed: %v", err)
				return
			}
			credsMap["access_token"] = newAccess
			credsMap["refresh_token"] = newRefresh
			newCredJSON, _ := json.Marshal(credsMap)
			encrypted, err := pkg.Encrypt(newCredJSON, encKey)
			if err != nil {
				log.Printf("[sync] encrypt failed for token persist: %v", err)
				return
			}
			db.DB.Model(&ch).Update("credentials_encrypted", encrypted)
			log.Printf("[sync] persisted refreshed Zalo tokens for channel %s", chID)
		})
	}

	// Determine since — use last_sync_at or default to 30 days ago for first sync
	// Subtract 1 hour buffer to avoid missing messages near the boundary
	since := time.Now().AddDate(0, 0, -30)
	if channel.LastSyncAt != nil {
		since = channel.LastSyncAt.Add(-1 * time.Hour)
	}

	// Fetch recent conversations
	conversations, err := adapter.FetchRecentConversations(ctx, since, 100)
	if err != nil {
		return s.updateSyncStatus(channel.ID, "error", fmt.Sprintf("fetch conversations failed: %v", err))
	}

	log.Printf("[sync] channel %s: found %d conversations", channel.Name, len(conversations))

	// Check if file sync is enabled for this channel
	syncFiles := false
	if channel.Metadata != "" {
		var meta map[string]interface{}
		if json.Unmarshal([]byte(channel.Metadata), &meta) == nil {
			if sf, ok := meta["sync_files"]; ok {
				syncFiles, _ = sf.(bool)
			}
		}
	}
	log.Printf("[sync] channel %s: sync_files=%v, metadata=%s", channel.Name, syncFiles, channel.Metadata)

	totalMessages := 0
	for _, conv := range conversations {
		// Upsert conversation
		convID, err := s.upsertConversation(channel.TenantID, channel.ID, conv)
		if err != nil {
			log.Printf("[sync] error upserting conversation %s: %v", conv.ExternalID, err)
			continue
		}

		// Fetch messages — if this conversation has no messages yet, don't filter by since
		// to ensure we get the initial batch of messages
		msgSince := since
		var existingMsgCount int64
		db.DB.Model(&models.Message{}).Where("conversation_id = ?", convID).Count(&existingMsgCount)
		if existingMsgCount == 0 {
			msgSince = time.Time{} // no since filter — fetch all available messages
		}
		messages, err := adapter.FetchMessages(ctx, conv.ExternalID, msgSince)
		if err != nil {
			log.Printf("[sync] error fetching messages for %s: %v", conv.ExternalID, err)
			continue
		}

		// For Pancake: agent messages have sender_name = page name (not staff name).
		// Replace with actual staff names from conversation metadata (current_assign_users).
		if channel.ChannelType == "pancake" && conv.Metadata != nil {
			agentName := ""
			if names, ok := conv.Metadata["_agent_names"].([]string); ok && len(names) > 0 {
				agentName = strings.Join(names, ", ")
			} else if names, ok := conv.Metadata["_agent_names"].([]interface{}); ok && len(names) > 0 {
				nameStrs := []string{}
				for _, n := range names {
					if s, ok := n.(string); ok {
						nameStrs = append(nameStrs, s)
					}
				}
				agentName = strings.Join(nameStrs, ", ")
			}
			if agentName != "" {
				for i := range messages {
					if messages[i].SenderType == "agent" {
						messages[i].SenderName = agentName
					}
				}
			}
		}

		// Upsert messages
		for _, msg := range messages {
			if syncFiles {
				s.downloadAttachments(channel.TenantID, convID, &msg)
			}
			if err := s.upsertMessage(channel.TenantID, convID, msg); err != nil {
				log.Printf("[sync] error upserting message %s: %v", msg.ExternalID, err)
			} else {
				totalMessages++
			}
		}

		// Update conversation message count and agent names
		var count int64
		db.DB.Model(&models.Message{}).Where("conversation_id = ?", convID).Count(&count)

		// Collect agent names from messages
		var agentNamesList []string
		db.DB.Model(&models.Message{}).
			Where("conversation_id = ? AND sender_type = 'agent' AND sender_name != ''", convID).
			Distinct("sender_name").
			Pluck("sender_name", &agentNamesList)

		// Also get agent names from conversation metadata (Pancake: current_assign_users)
		if conv.Metadata != nil {
			if metaNames, ok := conv.Metadata["_agent_names"].([]string); ok {
				for _, n := range metaNames {
					found := false
					for _, existing := range agentNamesList {
						if existing == n {
							found = true
							break
						}
					}
					if !found {
						agentNamesList = append(agentNamesList, n)
					}
				}
			}
			// Also handle []interface{} type from JSON
			if metaNames, ok := conv.Metadata["_agent_names"].([]interface{}); ok {
				for _, n := range metaNames {
					if name, ok := n.(string); ok && name != "" {
						found := false
						for _, existing := range agentNamesList {
							if existing == name {
								found = true
								break
							}
						}
						if !found {
							agentNamesList = append(agentNamesList, name)
						}
					}
				}
			}
		}

		agentNames := strings.Join(agentNamesList, ", ")

		db.DB.Model(&models.Conversation{}).Where("id = ?", convID).Updates(map[string]interface{}{
			"message_count": count,
			"agent_names":   agentNames,
		})
	}

	log.Printf("[sync] channel %s: synced %d conversations, %d messages", channel.Name, len(conversations), totalMessages)

	// Log activity
	db.LogActivity(channel.TenantID, "", "system", "sync.completed", "channel", channel.ID,
		fmt.Sprintf("Sync '%s': %d conversations, %d messages", channel.Name, len(conversations), totalMessages), "", "")

	// Trigger after-sync jobs for this channel
	if sched := GetDefaultScheduler(); sched != nil {
		sched.TriggerAfterSyncJobs(channel.TenantID, channel.ID)
	}

	return s.updateSyncStatus(channel.ID, "success", "")
}

// SyncAllChannels syncs all active channels for a tenant.
func (s *SyncEngine) SyncAllChannels(ctx context.Context, tenantID string) error {
	var chans []models.Channel
	db.DB.Where("tenant_id = ? AND is_active = true", tenantID).Find(&chans)

	for _, ch := range chans {
		if err := s.SyncChannel(ctx, ch); err != nil {
			log.Printf("[sync] channel %s failed: %v", ch.Name, err)
		}
	}
	return nil
}

func (s *SyncEngine) upsertConversation(tenantID, channelID string, conv channels.SyncedConversation) (string, error) {
	var existing models.Conversation
	result := db.DB.Where("tenant_id = ? AND channel_id = ? AND external_conversation_id = ?",
		tenantID, channelID, conv.ExternalID).First(&existing)

	metadataJSON, _ := json.Marshal(conv.Metadata)

	if result.Error == nil {
		// Update existing
		db.DB.Model(&existing).Updates(map[string]interface{}{
			"customer_name":   conv.CustomerName,
			"last_message_at": conv.LastMessageAt,
			"metadata":        string(metadataJSON),
			"updated_at":      time.Now(),
		})
		return existing.ID, nil
	}

	// Create new
	newConv := models.Conversation{
		ID:                       pkg.NewUUID(),
		TenantID:                 tenantID,
		ChannelID:                channelID,
		ExternalConversationID:   conv.ExternalID,
		ExternalUserID:           conv.ExternalUserID,
		CustomerName:             conv.CustomerName,
		LastMessageAt:            &conv.LastMessageAt,
		MessageCount:             0,
		Metadata:                 string(metadataJSON),
		CreatedAt:                time.Now(),
		UpdatedAt:                time.Now(),
	}
	if err := db.DB.Create(&newConv).Error; err != nil {
		return "", err
	}
	return newConv.ID, nil
}

func (s *SyncEngine) upsertMessage(tenantID, conversationID string, msg channels.SyncedMessage) error {
	// Check if message already exists (dedup by external_message_id)
	var existing models.Message
	result := db.DB.Where("tenant_id = ? AND conversation_id = ? AND external_message_id = ?",
		tenantID, conversationID, msg.ExternalID).First(&existing)
	if result.Error == nil {
		// Message exists — update attachments if we have new local paths
		hasLocalPath := false
		for _, att := range msg.Attachments {
			if att.LocalPath != "" {
				hasLocalPath = true
				break
			}
		}
		if hasLocalPath {
			attachmentsJSON, _ := json.Marshal(msg.Attachments)
			db.DB.Model(&existing).Update("attachments", string(attachmentsJSON))
		}
		return nil
	}

	attachmentsJSON, _ := json.Marshal(msg.Attachments)
	rawDataJSON, _ := json.Marshal(msg.RawData)

	message := models.Message{
		ID:                pkg.NewUUID(),
		TenantID:          tenantID,
		ConversationID:    conversationID,
		ExternalMessageID: msg.ExternalID,
		SenderType:        msg.SenderType,
		SenderName:        msg.SenderName,
		SenderExternalID:  msg.SenderExternalID,
		Content:           msg.Content,
		ContentType:       msg.ContentType,
		Attachments:       string(attachmentsJSON),
		SentAt:            msg.SentAt,
		RawData:           string(rawDataJSON),
		CreatedAt:         time.Now(),
	}
	return db.DB.Create(&message).Error
}

func (s *SyncEngine) updateSyncStatus(channelID, status, errMsg string) error {
	now := time.Now()
	updates := map[string]interface{}{
		"last_sync_at":     &now,
		"last_sync_status": status,
		"last_sync_error":  errMsg,
		"updated_at":       now,
	}
	db.DB.Model(&models.Channel{}).Where("id = ?", channelID).Updates(updates)
	if errMsg != "" {
		// Log error to activity logs
		var ch models.Channel
		if db.DB.Where("id = ?", channelID).First(&ch).Error == nil {
			db.LogActivity(ch.TenantID, "", "system", "sync.error", "channel", channelID, "Sync failed: "+ch.Name, errMsg, "")
		}
		return fmt.Errorf("sync failed: %s", errMsg)
	}
	return nil
}

// downloadAttachments downloads attachment files from URLs to local storage.
func (s *SyncEngine) downloadAttachments(tenantID, convID string, msg *channels.SyncedMessage) {
	for i, att := range msg.Attachments {
		if att.URL == "" {
			continue
		}
		// Create directory
		dir := filepath.Join("/var/lib/cqa/files", tenantID, convID)
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Printf("[sync] mkdir failed for %s: %v", dir, err)
			continue
		}

		// Generate filename — sanitize to prevent path traversal from external API data
		name := filepath.Base(att.Name)
		if name == "" || name == "." || name == "/" {
			name = fmt.Sprintf("%s-%d", att.Type, time.Now().UnixMilli())
		}
		localPath := filepath.Join(dir, name)
		// Verify path stays within intended directory
		if !strings.HasPrefix(filepath.Clean(localPath), filepath.Clean(dir)+string(filepath.Separator)) {
			log.Printf("[security] path traversal blocked: att.Name=%s resolved=%s", att.Name, localPath)
			continue
		}

		// Download file
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Get(att.URL)
		if err != nil {
			log.Printf("[sync] download failed for %s: %v", att.URL, err)
			continue
		}

		if resp.StatusCode != 200 {
			resp.Body.Close()
			log.Printf("[sync] download failed for %s: status %d", att.URL, resp.StatusCode)
			continue
		}

		f, err := os.Create(localPath)
		if err != nil {
			resp.Body.Close()
			log.Printf("[sync] create file failed %s: %v", localPath, err)
			continue
		}
		_, err = io.Copy(f, resp.Body)
		resp.Body.Close()
		f.Close()
		if err != nil {
			log.Printf("[sync] write file failed %s: %v", localPath, err)
			continue
		}

		// Update attachment with local path (relative for serving)
		msg.Attachments[i].LocalPath = filepath.Join(tenantID, convID, name)
		log.Printf("[sync] downloaded %s → %s", att.URL, localPath)
	}
}
