package discordbot

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Gateway owns the DiscordGo Gateway session and binds MessageCreate events
// to the testable adapter.
type Gateway struct {
	session *discordgo.Session
	adapter *Adapter
	ctx     context.Context
	cancel  context.CancelFunc
}

// GatewayClient is the Discord lifecycle boundary used by the application.
// Tests can provide a fake implementation without opening a Discord session.
type GatewayClient interface {
	Bind(*Adapter) error
	Open() error
	Close() error
	Sender() ReplySender
}

var _ GatewayClient = (*Gateway)(nil)

func NewGateway(token string) (GatewayClient, error) {
	if token == "" {
		return nil, errors.New("discordbot: token is required")
	}
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	session.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages
	ctx, cancel := context.WithCancel(context.Background())
	g := &Gateway{session: session, ctx: ctx, cancel: cancel}
	return g, nil
}

func (g *Gateway) Bind(adapter *Adapter) error {
	if g == nil || g.session == nil || adapter == nil {
		return errors.New("discordbot: Gateway and adapter are required")
	}
	g.adapter = adapter
	g.session.AddHandler(func(s *discordgo.Session, event *discordgo.MessageCreate) {
		if event == nil || event.Message == nil {
			return
		}
		m := event.Message
		incoming := Incoming{ID: m.ID, GuildID: m.GuildID, ChannelID: m.ChannelID, UserID: "", Content: m.Content, CreatedAt: m.Timestamp.UTC(), ReceivedAt: time.Now().UTC(), Webhook: m.WebhookID != "", System: m.Type != discordgo.MessageTypeDefault && m.Type != discordgo.MessageTypeReply, IsDM: m.GuildID == ""}
		if m.Author != nil {
			incoming.UserID = m.Author.ID
			incoming.AuthorBot = m.Author.Bot
		}
		if s.State != nil {
			if channel, err := s.State.Channel(m.ChannelID); err == nil && (channel.Type == discordgo.ChannelTypeGuildPublicThread || channel.Type == discordgo.ChannelTypeGuildPrivateThread || channel.Type == discordgo.ChannelTypeGuildNewsThread) {
				incoming.ThreadID = m.ChannelID
				if channel.ParentID != "" {
					incoming.ChannelID = channel.ParentID
				}
			}
		}
		botID := ""
		if s.State != nil && s.State.User != nil {
			botID = s.State.User.ID
		}
		go g.adapter.Handle(g.ctx, incoming, botID)
	})
	return nil
}

func (g *Gateway) Open() error { return g.session.Open() }
func (g *Gateway) Close() error {
	if g == nil || g.session == nil {
		return nil
	}
	if g.cancel != nil {
		g.cancel()
	}
	return g.session.Close()
}
func (g *Gateway) Sender() ReplySender { return discordSender{session: g.session} }

type discordSender struct{ session *discordgo.Session }

func (s discordSender) Reply(_ context.Context, replyTo, channelID, content string) (SentMessage, error) {
	sent, err := s.session.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{Content: content, Reference: &discordgo.MessageReference{MessageID: replyTo, ChannelID: channelID, FailIfNotExists: boolPointer(true)}, AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}})
	if err != nil {
		var rest *discordgo.RESTError
		if errors.As(err, &rest) && rest.Response != nil {
			delay, _ := strconv.ParseFloat(rest.Response.Header.Get("Retry-After"), 64)
			return SentMessage{}, discordAPIError{cause: err, status: rest.Response.StatusCode, retryAfter: time.Duration(delay * float64(time.Second))}
		}
		return SentMessage{}, err
	}
	return SentMessage{ID: sent.ID, CreatedAt: sent.Timestamp.UTC()}, nil
}

func (s discordSender) Edit(_ context.Context, channelID, messageID, content string) error {
	_, err := s.session.ChannelMessageEdit(channelID, messageID, content)
	if err != nil {
		var rest *discordgo.RESTError
		if errors.As(err, &rest) && rest.Response != nil {
			delay, _ := strconv.ParseFloat(rest.Response.Header.Get("Retry-After"), 64)
			return discordAPIError{cause: err, status: rest.Response.StatusCode, retryAfter: time.Duration(delay * float64(time.Second))}
		}
		return err
	}
	return nil
}

func boolPointer(value bool) *bool { return &value }

type discordAPIError struct {
	cause      error
	status     int
	retryAfter time.Duration
}

func (e discordAPIError) Error() string             { return e.cause.Error() }
func (e discordAPIError) Unwrap() error             { return e.cause }
func (e discordAPIError) StatusCode() int           { return e.status }
func (e discordAPIError) RetryAfter() time.Duration { return e.retryAfter }
