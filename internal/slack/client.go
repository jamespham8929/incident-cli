package slack

import (
	"fmt"

	slackgo "github.com/slack-go/slack"
)

type Client struct {
	api *slackgo.Client
}

type Channel struct {
	ID   string
	Name string
}

func NewClient(token string) *Client {
	return &Client{api: slackgo.New(token)}
}

func (c *Client) CreateChannel(name string) (*Channel, error) {
	ch, err := c.api.CreateConversation(slackgo.CreateConversationParams{
		ChannelName: name,
		IsPrivate:   false,
	})
	if err != nil {
		return nil, fmt.Errorf("slack CreateConversation: %w", err)
	}
	return &Channel{ID: ch.ID, Name: ch.Name}, nil
}

func (c *Client) FindChannel(name string) (*Channel, error) {
	channels, _, err := c.api.GetConversations(&slackgo.GetConversationsParameters{
		Limit: 200,
	})
	if err != nil {
		return nil, err
	}
	for _, ch := range channels {
		if ch.Name == name {
			return &Channel{ID: ch.ID, Name: ch.Name}, nil
		}
	}
	return nil, fmt.Errorf("channel %q not found", name)
}

func (c *Client) PostMessage(channelID, text string) error {
	_, _, err := c.api.PostMessage(channelID, slackgo.MsgOptionText(text, false))
	return err
}

func (c *Client) ArchiveChannel(channelID string) error {
	return c.api.ArchiveConversation(channelID)
}

func (c *Client) InviteUsers(channelID string, userIDs []string) error {
	_, err := c.api.InviteUsersToConversation(channelID, userIDs...)
	return err
}
