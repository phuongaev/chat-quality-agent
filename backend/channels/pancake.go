package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	pancakeConversationsBase = "https://pages.fm/api/public_api/v2"
	pancakeMessagesBase      = "https://pages.fm/api/public_api/v1"
)

// PancakeCredentials holds credentials for Pancake (pages.fm) API.
type PancakeCredentials struct {
	PageID      string `json:"page_id"`
	AccessToken string `json:"access_token"`
}

type PancakeAdapter struct {
	creds  PancakeCredentials
	client *http.Client
}

func NewPancakeAdapter(creds PancakeCredentials) *PancakeAdapter {
	return &PancakeAdapter{
		creds:  creds,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (p *PancakeAdapter) doRequest(ctx context.Context, url string) (map[string]interface{}, error) {
	maxRetries := 3
	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("create pancake api request: %w", err)
		}

		q := req.URL.Query()
		if q.Get("access_token") == "" {
			q.Set("access_token", p.creds.AccessToken)
			req.URL.RawQuery = q.Encode()
		}

		resp, err := p.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("pancake api request failed: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("pancake api read body failed: %w", err)
		}

		// Handle rate limiting with retry
		if resp.StatusCode == 429 && attempt < maxRetries {
			wait := time.Duration(2+attempt*3) * time.Second // 2s, 5s, 8s
			log.Printf("[pancake] rate limited (429), waiting %v before retry %d/%d", wait, attempt+1, maxRetries)
			select {
			case <-time.After(wait):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("pancake api error: status %d, body: %s", resp.StatusCode, string(body))
		}

		var result map[string]interface{}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("pancake api decode failed: %w", err)
		}

		if errMsg, ok := result["error"].(string); ok && errMsg != "" {
			return nil, fmt.Errorf("pancake api error: %s", errMsg)
		}

		// Throttle: small delay between requests to avoid rate limiting
		time.Sleep(500 * time.Millisecond)

		return result, nil
	}
	return nil, fmt.Errorf("pancake api: max retries exceeded")
}

func (p *PancakeAdapter) FetchRecentConversations(ctx context.Context, since time.Time, limit int) ([]SyncedConversation, error) {
	var conversations []SyncedConversation
	seenIDs := make(map[string]bool)
	lastConvID := ""

	// Build base URL — use access_token (verified working) + order by updated_at
	baseURL := fmt.Sprintf("%s/pages/%s/conversations?access_token=%s&order_by=updated_at",
		pancakeConversationsBase, p.creds.PageID, p.creds.AccessToken)
	if !since.IsZero() {
		baseURL += fmt.Sprintf("&since=%d", since.Unix())
	}
	log.Printf("[pancake] fetching conversations: since=%v, limit=%d", since, limit)

	batch := 0
	for {
		if limit > 0 && len(conversations) >= limit {
			break
		}

		// Cursor-based pagination: pass last_conversation_id from previous batch
		fetchURL := baseURL
		if lastConvID != "" {
			fetchURL += "&last_conversation_id=" + lastConvID
		}

		result, err := p.doRequest(ctx, fetchURL)
		if err != nil {
			return conversations, err
		}

		// Pancake returns conversations under "conversations" key
		data, ok := result["data"].([]interface{})
		if !ok {
			data, ok = result["conversations"].([]interface{})
		}
		if !ok || len(data) == 0 {
			if batch == 0 {
				// Log why first batch returned empty
				topKeys := make([]string, 0, len(result))
				for k := range result {
					topKeys = append(topKeys, k)
				}
				log.Printf("[pancake] first batch empty, response keys: %v, success=%v", topKeys, result["success"])
			}
			break
		}

		prevCount := len(conversations)
		var batchLastID string
		for _, item := range data {
			conv, ok := item.(map[string]interface{})
			if !ok {
				continue
			}

			convID, _ := conv["id"].(string)
			if convID == "" {
				if numID, ok := conv["id"].(float64); ok {
					convID = fmt.Sprintf("%.0f", numID)
				}
			}
			batchLastID = convID

			// Skip duplicates
			if seenIDs[convID] {
				continue
			}
			seenIDs[convID] = true

			// Parse updated_at
			updatedAt := parsePancakeTime(conv, "updated_at")
			if updatedAt.IsZero() {
				updatedAt = parsePancakeTime(conv, "inserted_at")
			}

			// Extract customer name from customers[] array
			customerName := ""
			if customers, ok := conv["customers"].([]interface{}); ok && len(customers) > 0 {
				if c0, ok := customers[0].(map[string]interface{}); ok {
					customerName, _ = c0["name"].(string)
				}
			}
			if customerName == "" {
				if from, ok := conv["from"].(map[string]interface{}); ok {
					customerName, _ = from["name"].(string)
				}
			}

			externalUserID, _ := conv["customer_id"].(string)

			// Store assignee (staff) names in metadata for later use
			if assignUsers, ok := conv["current_assign_users"].([]interface{}); ok {
				names := []string{}
				for _, u := range assignUsers {
					if user, ok := u.(map[string]interface{}); ok {
						if name, ok := user["name"].(string); ok && name != "" {
							names = append(names, name)
						}
					}
				}
				if len(names) > 0 {
					conv["_agent_names"] = names
				}
			}

			conversations = append(conversations, SyncedConversation{
				ExternalID:     convID,
				ExternalUserID: externalUserID,
				CustomerName:   customerName,
				LastMessageAt:  updatedAt,
				Metadata:       conv,
			})

			if limit > 0 && len(conversations) >= limit {
				break
			}
		}

		batch++
		newCount := len(conversations) - prevCount
		log.Printf("[pancake] batch %d: %d items, %d new, total: %d/%d",
			batch, len(data), newCount, len(conversations), limit)

		// No new conversations in this batch or cursor didn't advance — stop
		if batchLastID == "" || batchLastID == lastConvID {
			break
		}
		lastConvID = batchLastID

		// Less than 60 items means we've reached the end
		if len(data) < 60 {
			break
		}
	}

	return conversations, nil
}

func (p *PancakeAdapter) FetchMessages(ctx context.Context, conversationID string, since time.Time) ([]SyncedMessage, error) {
	var messages []SyncedMessage

	url := fmt.Sprintf("%s/pages/%s/conversations/%s/messages?access_token=%s",
		pancakeMessagesBase, p.creds.PageID, conversationID, p.creds.AccessToken)

	// Pancake uses current_count for pagination:
	// - Without current_count: returns ~20 most recent messages
	// - With current_count=N: returns ~25 messages starting from position N
	currentCount := 0

	for {
		fetchURL := url
		if currentCount > 0 {
			fetchURL = fmt.Sprintf("%s&current_count=%d", url, currentCount)
		}

		result, err := p.doRequest(ctx, fetchURL)
		if err != nil {
			return messages, err
		}

		// Pancake returns messages under "messages" key (not "data")
		data, ok := result["data"].([]interface{})
		if !ok {
			data, ok = result["messages"].([]interface{})
		}
		if !ok || len(data) == 0 {
			break
		}

		reachedOld := false
		for _, item := range data {
			msg, ok := item.(map[string]interface{})
			if !ok {
				continue
			}

			sentAt := parsePancakeTime(msg, "inserted_at")

			// Only filter by since if we successfully parsed a timestamp
			if !sentAt.IsZero() && !since.IsZero() && sentAt.Before(since) {
				reachedOld = true
				break
			}

			// Skip hidden/removed messages
			if isHidden, ok := msg["is_hidden"].(bool); ok && isHidden {
				continue
			}
			if isRemoved, ok := msg["is_removed"].(bool); ok && isRemoved {
				continue
			}

			msgID, _ := msg["id"].(string)
			if msgID == "" {
				if numID, ok := msg["id"].(float64); ok {
					msgID = fmt.Sprintf("%.0f", numID)
				}
			}
			if msgID == "" {
				continue
			}

			content, _ := msg["message"].(string)
			content = stripHTMLTags(content)
			// Fallback to original_message if message was only HTML tags
			if content == "" {
				if orig, ok := msg["original_message"].(string); ok {
					content = stripHTMLTags(orig)
				}
			}

			// Determine sender info
			senderType := "customer"
			senderName := ""
			senderExternalID := ""
			if from, ok := msg["from"].(map[string]interface{}); ok {
				senderName, _ = from["name"].(string)
				if fromID, ok := from["id"].(string); ok {
					senderExternalID = fromID
				} else if fromIDNum, ok := from["id"].(float64); ok {
					senderExternalID = fmt.Sprintf("%.0f", fromIDNum)
				}

				// Messages sent by the page (page_id matches from.id) → agent
				pageID, _ := msg["page_id"].(string)
				if pageID == "" {
					if pid, ok := msg["page_id"].(float64); ok {
						pageID = fmt.Sprintf("%.0f", pid)
					}
				}
				if senderExternalID != "" && senderExternalID == pageID {
					senderType = "agent"
				}
			}

			msgType, _ := msg["type"].(string)
			contentType := "text"
			if msgType == "image" || msgType == "sticker" || msgType == "file" || msgType == "video" {
				contentType = msgType
			}

			messages = append(messages, SyncedMessage{
				ExternalID:       msgID,
				SenderType:       senderType,
				SenderName:       senderName,
				SenderExternalID: senderExternalID,
				Content:          content,
				ContentType:      contentType,
				SentAt:           sentAt,
				RawData:          msg,
			})
		}

		if reachedOld {
			break
		}

		// Pagination
		batchSize := len(data)
		if currentCount == 0 {
			currentCount = batchSize
		} else {
			currentCount += batchSize
		}

		if batchSize < 20 {
			break
		}
	}

	if len(messages) > 0 {
		log.Printf("[pancake] fetched %d messages for conversation %s", len(messages), conversationID)
	}
	return messages, nil
}

func (p *PancakeAdapter) HealthCheck(ctx context.Context) error {
	url := fmt.Sprintf("%s/pages/%s/conversations?access_token=%s",
		pancakeConversationsBase, p.creds.PageID, p.creds.AccessToken)
	_, err := p.doRequest(ctx, url)
	return err
}

// parsePancakeTime parses a timestamp from a Pancake API response field.
// Pancake uses formats like "2026-04-06T14:24:19" (no timezone) or with microseconds.
func parsePancakeTime(obj map[string]interface{}, field string) time.Time {
	if str, ok := obj[field].(string); ok && str != "" {
		layouts := []string{
			time.RFC3339,
			"2006-01-02T15:04:05",       // Pancake common format (no timezone)
			"2006-01-02T15:04:05.000000", // with microseconds
			"2006-01-02T15:04:05.000",    // with milliseconds
			"2006-01-02T15:04:05Z",
			"2006-01-02T15:04:05-0700",
			"2006-01-02 15:04:05",
		}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, str); err == nil {
				return t
			}
		}
	}
	if ts, ok := obj[field].(float64); ok && ts > 0 {
		return time.Unix(int64(ts), 0)
	}
	return time.Time{}
}

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// stripHTMLTags removes HTML tags and decodes common entities from a string.
func stripHTMLTags(s string) string {
	s = htmlTagRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	return strings.TrimSpace(s)
}
