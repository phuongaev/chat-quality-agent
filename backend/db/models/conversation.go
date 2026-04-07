package models

import "time"

type Conversation struct {
	ID                     string     `gorm:"type:char(36);primaryKey" json:"id"`
	TenantID               string     `gorm:"type:char(36);not null;index:idx_conv_tenant_last_msg" json:"tenant_id"`
	ChannelID              string     `gorm:"type:char(36);not null;index:idx_conv_channel" json:"channel_id"`
	ExternalConversationID string     `gorm:"type:varchar(255);not null" json:"external_conversation_id"`
	ExternalUserID         string     `gorm:"type:varchar(255)" json:"external_user_id"`
	CustomerName           string     `gorm:"type:varchar(500)" json:"customer_name"`
	LastMessageAt          *time.Time `gorm:"index:idx_conv_tenant_last_msg" json:"last_message_at"`
	MessageCount           int        `gorm:"default:0" json:"message_count"`
	AgentNames             string     `gorm:"type:varchar(1000)" json:"agent_names"`
	Metadata               string     `gorm:"type:json" json:"metadata"`
	CreatedAt              time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt              time.Time  `gorm:"not null" json:"updated_at"`

	Channel  Channel   `gorm:"foreignKey:ChannelID" json:"channel,omitempty"`
	Messages []Message `gorm:"foreignKey:ConversationID" json:"messages,omitempty"`
}
