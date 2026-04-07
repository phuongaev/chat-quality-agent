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
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("pancake api read body failed: %w", err)
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

	return result, nil
}

func (p *PancakeAdapter) FetchRecentConversations(ctx context.Context, since time.Time, limit int) ([]SyncedConversation, error) {
	var conversations []SyncedConversation

	pageNum := 1
	for {
		if limit > 0 && len(conversations) >= limit {
			break
		}

		url := fmt.Sprintf("%s/pages/%s/conversations?access_token=%s&page=%d",
			pancakeConversationsBase, p.creds.PageID, p.creds.AccessToken, pageNum)

		result, err := p.doRequest(ctx, url)
		if err != nil {
			return conversations, err
		}

		// Pancake returns conversations under "conversations" key (not "data")
		data, ok := result["data"].([]interface{})
		if !ok {
			data, ok = result["conversations"].([]interface{})
		}
		if !ok || len(data) == 0 {
			break
		}

		reachedOld := false
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

			// Parse updated_at — Pancake format: "2026-04-06T14:24:19" (no timezone)
			updatedAt := parsePancakeTime(conv, "updated_at")
			if updatedAt.IsZero() {
				updatedAt = parsePancakeTime(conv, "inserted_at")
			}

			// Only filter by since if we successfully parsed a timestamp
			if !updatedAt.IsZero() && !since.IsZero() && updatedAt.Before(since) {
				reachedOld = true
				break
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
		}

		log.Printf("[pancake] page %d: got %d items, total so far: %d, reachedOld: %v, limit: %d",
			pageNum, len(data), len(conversations), reachedOld, limit)

		if reachedOld {
			break
		}

		// Pancake returns ~60 items per page; if less, we've reached the last page
		if len(data) < 50 {
			break
		}

		pageNum++
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
	url := fmt.Sprintf("%s/pages/%s/conversations?access_token=%s&page=1",
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
