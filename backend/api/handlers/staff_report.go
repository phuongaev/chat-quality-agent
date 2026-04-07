package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vietbui/chat-quality-agent/api/middleware"
	"github.com/vietbui/chat-quality-agent/db"
	"github.com/vietbui/chat-quality-agent/db/models"
)

type StaffReportItem struct {
	Name                   string            `json:"name"`
	SenderExternalID       string            `json:"sender_external_id"`
	TotalConversations     int               `json:"total_conversations"`
	TotalMessages          int               `json:"total_messages"`
	EvaluatedConversations int               `json:"evaluated_conversations"`
	PassCount              int               `json:"pass_count"`
	FailCount              int               `json:"fail_count"`
	PassRate               float64           `json:"pass_rate"`
	Violations             map[string]int    `json:"violations"`
}

func GetStaffReport(c *gin.Context) {
	tenantID := middleware.GetTenantID(c)

	// Parse date range
	fromStr := c.Query("from")
	toStr := c.Query("to")
	channelID := c.Query("channel_id")
	jobID := c.Query("job_id")

	var fromDate, toDate time.Time
	if fromStr != "" {
		fromDate, _ = time.Parse("2006-01-02", fromStr)
	} else {
		fromDate = time.Now().AddDate(0, -1, 0) // default: last month
	}
	if toStr != "" {
		toDate, _ = time.Parse("2006-01-02", toStr)
		toDate = toDate.Add(24*time.Hour - time.Second)
	} else {
		toDate = time.Now()
	}

	// Step 1: Get all unique agents from messages in the time range
	type agentInfo struct {
		SenderName string
		ConvCount  int64
		MsgCount   int64
	}

	msgQuery := db.DB.Model(&models.Message{}).
		Select("sender_name, COUNT(DISTINCT conversation_id) as conv_count, COUNT(*) as msg_count").
		Where("tenant_id = ? AND sender_type = 'agent' AND sender_name != '' AND sent_at >= ? AND sent_at <= ?",
			tenantID, fromDate, toDate).
		Group("sender_name")

	// Filter by channel if specified
	if channelID != "" {
		msgQuery = msgQuery.Where("conversation_id IN (SELECT id FROM conversations WHERE channel_id = ?)", channelID)
	}

	var agents []agentInfo
	msgQuery.Find(&agents)

	if len(agents) == 0 {
		c.JSON(http.StatusOK, gin.H{"staff": []interface{}{}, "from": fromDate, "to": toDate})
		return
	}

	// Step 2: For each agent, get their conversation IDs
	results := make([]StaffReportItem, 0, len(agents))

	for _, agent := range agents {
		// Get conversation IDs for this agent
		var convIDs []string
		convQuery := db.DB.Model(&models.Message{}).
			Where("tenant_id = ? AND sender_type = 'agent' AND sender_name = ? AND sent_at >= ? AND sent_at <= ?",
				tenantID, agent.SenderName, fromDate, toDate)
		if channelID != "" {
			convQuery = convQuery.Where("conversation_id IN (SELECT id FROM conversations WHERE channel_id = ?)", channelID)
		}
		convQuery.Distinct("conversation_id").Pluck("conversation_id", &convIDs)

		if len(convIDs) == 0 {
			continue
		}

		// Step 3: Get evaluation results for these conversations
		evalQuery := db.DB.Model(&models.JobResult{}).
			Where("tenant_id = ? AND conversation_id IN ? AND result_type = 'conversation_evaluation'",
				tenantID, convIDs)
		if jobID != "" {
			evalQuery = evalQuery.Where("job_run_id IN (SELECT id FROM job_runs WHERE job_id = ?)", jobID)
		}

		// Get latest evaluation per conversation
		type evalResult struct {
			ConversationID string
			Severity       string
		}
		var evals []evalResult
		db.DB.Raw(`
			SELECT jr.conversation_id, jr.severity
			FROM job_results jr
			INNER JOIN (
				SELECT conversation_id, MAX(created_at) as max_created
				FROM job_results
				WHERE tenant_id = ? AND result_type = 'conversation_evaluation'
					AND conversation_id IN ?
				GROUP BY conversation_id
			) latest ON jr.conversation_id = latest.conversation_id AND jr.created_at = latest.max_created
			WHERE jr.tenant_id = ? AND jr.result_type = 'conversation_evaluation'
		`, tenantID, convIDs, tenantID).Scan(&evals)

		passCount := 0
		failCount := 0
		for _, e := range evals {
			if e.Severity == "PASS" {
				passCount++
			} else {
				failCount++
			}
		}

		// Get violation breakdown by severity
		type violationCount struct {
			Severity string
			Count    int64
		}
		var violations []violationCount
		violQuery := db.DB.Model(&models.JobResult{}).
			Select("severity, COUNT(*) as count").
			Where("tenant_id = ? AND conversation_id IN ? AND result_type = 'qc_violation'",
				tenantID, convIDs).
			Group("severity")
		if jobID != "" {
			violQuery = violQuery.Where("job_run_id IN (SELECT id FROM job_runs WHERE job_id = ?)", jobID)
		}
		violQuery.Find(&violations)

		violMap := make(map[string]int)
		for _, v := range violations {
			violMap[v.Severity] = int(v.Count)
		}

		passRate := 0.0
		if len(evals) > 0 {
			passRate = float64(passCount) / float64(len(evals)) * 100
		}

		results = append(results, StaffReportItem{
			Name:                   agent.SenderName,
			TotalConversations:     int(agent.ConvCount),
			TotalMessages:          int(agent.MsgCount),
			EvaluatedConversations: len(evals),
			PassCount:              passCount,
			FailCount:              failCount,
			PassRate:               passRate,
			Violations:             violMap,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"staff": results,
		"from":  fromDate.Format("2006-01-02"),
		"to":    toDate.Format("2006-01-02"),
	})
}
