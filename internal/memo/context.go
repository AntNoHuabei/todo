package memo

import "context"

type chatIDContextKey struct{}

func WithChatID(ctx context.Context, chatID string) context.Context {
	if chatID == "" {
		return ctx
	}
	return context.WithValue(ctx, chatIDContextKey{}, chatID)
}

func ChatIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	chatID, _ := ctx.Value(chatIDContextKey{}).(string)
	return chatID
}
