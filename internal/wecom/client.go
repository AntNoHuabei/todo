package wecom

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-sphere/wecom-aibot-go-sdk/aibot"

	"todo-assistant/internal/config"
)

type InboundText struct {
	Frame  *aibot.WsFrame
	MsgID  string
	ChatID string
	UserID string
	Text   string
}

type Client struct {
	cfg             config.WeComConfig
	logger          *log.Logger
	ws              *aibot.WSClient
	onText          func(context.Context, InboundText)
	sendMu          sync.Mutex
	lastSend        time.Time
	minSendInterval time.Duration
}

func NewClient(cfg config.WeComConfig, logger *log.Logger) *Client {
	if logger == nil {
		logger = log.Default()
	}
	return &Client{cfg: cfg, logger: logger, minSendInterval: time.Second}
}

func (c *Client) OnText(fn func(context.Context, InboundText)) {
	c.onText = fn
}

func (c *Client) Run(ctx context.Context) error {
	c.ws = aibot.NewWSClient(aibot.WSClientOptions{
		BotID:                c.cfg.BotID,
		Secret:               c.cfg.Secret,
		WSURL:                c.cfg.WebSocketURL,
		MaxReconnectAttempts: -1,
		Logger: aibot.NewLoggerFunc(func(level, format string, v ...interface{}) {
			c.logger.Printf("[wecom:%s] "+format, append([]interface{}{level}, v...)...)
		}),
	})
	c.ws.OnMessageText(func(frame *aibot.WsFrame) {
		var msg aibot.TextMessage
		if err := aibot.ParseMessageBody(frame, &msg); err != nil {
			c.logger.Printf("parse text message failed: %v", err)
			return
		}
		if c.onText != nil {
			c.onText(ctx, InboundText{
				Frame:  frame,
				MsgID:  msg.MsgID,
				ChatID: msg.ChatID,
				UserID: msg.From.UserID,
				Text:   msg.Text.Content,
			})
		}
	})
	c.ws.OnError(func(err error) {
		c.logger.Printf("wecom error: %v", err)
	})
	c.ws.Connect()
	<-ctx.Done()
	c.ws.Disconnect()
	return ctx.Err()
}

func (c *Client) ReplyText(ctx context.Context, msg InboundText, content string) error {
	if c.ws == nil {
		return nil
	}
	if err := c.waitOutboundTurn(ctx); err != nil {
		return err
	}
	_, err := c.ws.Reply(msg.Frame, aibot.CreateTextReplyBody(content), aibot.WsCmd.RESPONSE)
	return err
}

func (c *Client) ReplyStream(ctx context.Context, msg InboundText, streamID, content string, finish bool) error {
	if c.ws == nil {
		return nil
	}
	if err := c.waitOutboundTurn(ctx); err != nil {
		return err
	}
	_, err := c.ws.ReplyStream(msg.Frame, streamID, strings.TrimSpace(content), finish, nil, nil)
	return err
}

func (c *Client) ReplyTextStreamed(ctx context.Context, msg InboundText, streamID, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		content = "我在。"
	}
	chunks := cumulativeChunks(content, 60)
	for i, chunk := range chunks {
		finish := i == len(chunks)-1
		if err := c.ReplyStream(ctx, msg, streamID, chunk, finish); err != nil {
			return err
		}
		if !finish {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.minSendInterval):
			}
		}
	}
	return nil
}

func cumulativeChunks(text string, step int) []string {
	if step <= 0 {
		step = 60
	}
	if utf8.RuneCountInString(text) <= step {
		return []string{text}
	}
	runes := []rune(text)
	out := make([]string, 0, len(runes)/step+1)
	for n := step; n < len(runes); n += step {
		out = append(out, string(runes[:n]))
	}
	out = append(out, text)
	return out
}

func (c *Client) SendText(ctx context.Context, chatID, content string) error {
	if c.ws == nil {
		return nil
	}
	if err := c.waitOutboundTurn(ctx); err != nil {
		return err
	}
	_, err := c.ws.SendMessage(chatID, aibot.CreateTextReplyBody(content))
	return err
}

func (c *Client) SendMarkdown(ctx context.Context, chatID, content string) error {
	if c.ws == nil {
		return nil
	}
	if err := c.waitOutboundTurn(ctx); err != nil {
		return err
	}
	_, err := c.ws.SendMarkdown(chatID, strings.TrimSpace(content))
	return err
}

func (c *Client) waitOutboundTurn(ctx context.Context) error {
	if c.minSendInterval <= 0 {
		return nil
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	if !c.lastSend.IsZero() {
		wait := c.minSendInterval - time.Since(c.lastSend)
		if wait > 0 {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	c.lastSend = time.Now()
	return nil
}
