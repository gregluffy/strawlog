package hub

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPayloadShapes(t *testing.T) {
	evs := []Event{{Type: "node.down", Severity: "critical", NodeName: "vm-1", Title: "Node is offline", Text: strings.Repeat("x", 5000), Fields: [][2]string{{"Address", "10.0.0.5:7443"}}, At: time.Now()}}

	b, _ := json.Marshal(discordPayload(evs, "https://logs.lan"))
	var d struct {
		Embeds []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Color       int    `json:"color"`
			URL         string `json:"url"`
		} `json:"embeds"`
		AllowedMentions map[string][]string `json:"allowed_mentions"`
	}
	if err := json.Unmarshal(b, &d); err != nil || len(d.Embeds) != 1 {
		t.Fatalf("discord: %v %s", err, b)
	}
	if d.Embeds[0].Color != 0xEF5F67 || !strings.Contains(d.Embeds[0].Title, "[vm-1]") || len(d.Embeds[0].Description) > 4096 || d.Embeds[0].URL == "" {
		t.Fatalf("discord embed: %+v", d.Embeds[0])
	}
	if d.AllowedMentions == nil {
		t.Fatal("mentions must be disabled (no @everyone from log text)")
	}

	b, _ = json.Marshal(teamsPayload(evs, ""))
	var tm struct {
		Type        string `json:"type"`
		Attachments []struct {
			ContentType string `json:"contentType"`
			Content     struct {
				Type    string `json:"type"`
				Version string `json:"version"`
				Body    []any  `json:"body"`
			} `json:"content"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(b, &tm); err != nil || tm.Type != "message" || len(tm.Attachments) != 1 {
		t.Fatalf("teams: %v %s", err, b)
	}
	a := tm.Attachments[0]
	if a.ContentType != "application/vnd.microsoft.card.adaptive" || a.Content.Type != "AdaptiveCard" || len(a.Content.Body) != 1 {
		t.Fatalf("teams card: %+v", a)
	}
}
