package ai

import "fmt"

// BuildQCPrompt creates the system prompt for QC analysis.
func BuildQCPrompt(rulesContent, skipConditions string) string {
	skipSection := ""
	if skipConditions != "" {
		skipSection = fmt.Sprintf(`
## Điều kiện bỏ qua (không đánh giá):
%s

Nếu cuộc chat thỏa mãn bất kỳ điều kiện nào trên, trả về verdict="SKIP", violations=[], score=0, review=lý do bỏ qua ngắn gọn.
`, skipConditions)
	}

	return fmt.Sprintf(`Bạn là chuyên gia đánh giá chất lượng chăm sóc khách hàng (CSKH).

## Quy định CSKH cần tuân thủ:
%s
%s
## Nhiệm vụ:
Phân tích đoạn chat CSKH dưới đây và tìm các vi phạm quy định.

## Yêu cầu output:
Trả về JSON với cấu trúc sau:
{
  "verdict": "PASS", "FAIL" hoặc "SKIP",
  "score": 0-100,
  "review": "Nhận xét tổng quan cuộc chat: chat tốt hay chưa tốt, cần cải thiện điều gì",
  "violations": [
    {
      "severity": "NGHIEM_TRONG" hoặc "CAN_CAI_THIEN",
      "rule": "Tên quy tắc bị vi phạm",
      "evidence": "Trích dẫn chính xác đoạn chat vi phạm",
      "explanation": "Giải thích ngắn gọn tại sao đây là vi phạm",
      "suggestion": "Gợi ý cách trả lời đúng"
    }
  ],
  "customer_sentiment": {
    "score": -100 đến 100,
    "label": "VERY_NEGATIVE" | "NEGATIVE" | "NEUTRAL" | "POSITIVE" | "VERY_POSITIVE",
    "reason": "Giải thích ngắn gọn tại sao khách hàng có cảm xúc này"
  },
  "summary": "Tổng quan ngắn gọn về chất lượng chat"
}

- "verdict": "PASS" nếu cuộc chat đạt yêu cầu chất lượng, "FAIL" nếu có vấn đề cần khắc phục, "SKIP" nếu thỏa điều kiện bỏ qua
- "review": Nhận xét chi tiết về cuộc chat (2-3 câu), đánh giá chất lượng chăm sóc khách hàng
- "customer_sentiment": Phân tích cảm xúc của khách hàng trong cuộc chat:
  - score: -100 (rất tiêu cực) đến 100 (rất tích cực), 0 là trung tính
  - label: nhãn cảm xúc tổng quan
  - reason: lý do khách có cảm xúc này (dựa trên nội dung chat, từ ngữ, thái độ)
- Nếu không có vi phạm: verdict="PASS", violations=[], score gần 100
- Nếu có vi phạm nghiêm trọng: verdict="FAIL"
CHỈ trả về JSON, không thêm text khác.`, rulesContent, skipSection)
}

// BuildClassificationPrompt creates the system prompt for conversation classification.
func BuildClassificationPrompt(rulesConfigJSON string) string {
	return fmt.Sprintf(`Bạn là hệ thống phân loại nội dung hội thoại CSKH/Sales.

## Các quy tắc phân loại:
%s

## Nhiệm vụ:
Phân tích đoạn chat dưới đây và gán các nhãn phân loại phù hợp.

## Yêu cầu output:
Trả về JSON:
{
  "tags": [
    {
      "rule_name": "Tên rule đã match",
      "confidence": 0.0-1.0,
      "evidence": "Trích dẫn đoạn chat liên quan",
      "explanation": "Giải thích ngắn gọn tại sao"
    }
  ],
  "summary": "Mô tả chi tiết nội dung cuộc chat: khách hàng nói gì, nhân viên xử lý ra sao, kết quả thế nào (2-3 câu, KHÔNG lặp lại tên nhãn phân loại)"
}

- "summary" phải mô tả CỤ THỂ nội dung cuộc chat, không được viết chung chung như "Cuộc chat được phân loại: X"
- Ví dụ tốt: "Khách hàng hỏi về tính năng webhook nhưng nhân viên không nắm rõ, hướng dẫn sai cách cấu hình. Khách phản hồi tiêu cực."
- Ví dụ xấu: "Cuộc chat được phân loại: Góp ý tính năng"
CHỈ trả về JSON, không thêm text khác.`, rulesConfigJSON)
}

// FormatBatchTranscript formats multiple conversations for batch analysis.
func FormatBatchTranscript(items []BatchItem) string {
	result := ""
	for i, item := range items {
		result += fmt.Sprintf("=== CUỘC HỘI THOẠI %d (ID: %s) ===\n%s\n\n", i+1, item.ConversationID, item.Transcript)
	}
	return result
}

// WrapBatchPrompt wraps a single-conversation system prompt into a batch prompt.
func WrapBatchPrompt(basePrompt string, count int) string {
	return fmt.Sprintf(`%s

QUAN TRỌNG: Bạn sẽ nhận được %d cuộc hội thoại, mỗi cuộc được đánh dấu "=== CUỘC HỘI THOẠI N (ID: xxx) ===".
Trả về JSON ARRAY chứa %d phần tử, mỗi phần tử là kết quả đánh giá cho 1 cuộc hội thoại theo đúng thứ tự.
Format: [{"conversation_id": "xxx", ...kết quả...}, ...]
CHỈ trả về JSON array, không thêm text khác.`, basePrompt, count, count)
}

// FormatChatTranscript formats messages into a readable transcript for AI analysis.
func FormatChatTranscript(messages []ChatMessage) string {
	result := ""
	for _, msg := range messages {
		label := msg.SenderName
		if label == "" {
			label = msg.SenderType
		}
		result += fmt.Sprintf("[%s] %s: %s\n", msg.SentAt, label, msg.Content)
	}
	return result
}

// ChatMessage is a simplified message for transcript formatting.
type ChatMessage struct {
	SenderType string
	SenderName string
	Content    string
	SentAt     string
}
