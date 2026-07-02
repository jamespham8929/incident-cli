package slack

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	slackgo "github.com/slack-go/slack"
	"github.com/stretchr/testify/require"
)

// routeServer starts an httptest server that maps a request path suffix (the
// Slack method name, e.g. "conversations.create") to a canned JSON body, and
// returns a Client pointed at it. Slack signals API errors with an ok:false
// body rather than an HTTP status, so the error cases return 200 too.
func routeServer(t *testing.T, routes map[string]string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for suffix, body := range routes {
			if strings.HasSuffix(r.URL.Path, suffix) {
				_, _ = w.Write([]byte(body))
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return &Client{api: slackgo.New("test-token", slackgo.OptionAPIURL(srv.URL+"/"))}
}

func TestCreateChannel(t *testing.T) {
	c := routeServer(t, map[string]string{
		"conversations.create": `{"ok":true,"channel":{"id":"C123","name":"inc-1"}}`,
	})

	ch, err := c.CreateChannel("inc-1")
	require.NoError(t, err)
	require.Equal(t, "C123", ch.ID)
	require.Equal(t, "inc-1", ch.Name)
}

func TestCreateChannelError(t *testing.T) {
	c := routeServer(t, map[string]string{
		"conversations.create": `{"ok":false,"error":"name_taken"}`,
	})

	_, err := c.CreateChannel("inc-1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "slack CreateConversation")
	require.Contains(t, err.Error(), "name_taken")
}

func TestFindChannel(t *testing.T) {
	c := routeServer(t, map[string]string{
		"conversations.list": `{"ok":true,"channels":[{"id":"C1","name":"inc-1"},{"id":"C2","name":"inc-2"}],"response_metadata":{"next_cursor":""}}`,
	})

	ch, err := c.FindChannel("inc-2")
	require.NoError(t, err)
	require.Equal(t, "C2", ch.ID)
}

func TestFindChannelNotFound(t *testing.T) {
	c := routeServer(t, map[string]string{
		"conversations.list": `{"ok":true,"channels":[{"id":"C1","name":"inc-1"}],"response_metadata":{"next_cursor":""}}`,
	})

	_, err := c.FindChannel("missing")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestFindChannelError(t *testing.T) {
	c := routeServer(t, map[string]string{
		"conversations.list": `{"ok":false,"error":"invalid_auth"}`,
	})

	_, err := c.FindChannel("inc-1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_auth")
}

func TestPostMessage(t *testing.T) {
	c := routeServer(t, map[string]string{
		"chat.postMessage": `{"ok":true,"channel":"C1","ts":"123.456"}`,
	})
	require.NoError(t, c.PostMessage("C1", "incident declared"))
}

func TestPostMessageError(t *testing.T) {
	c := routeServer(t, map[string]string{
		"chat.postMessage": `{"ok":false,"error":"channel_not_found"}`,
	})

	err := c.PostMessage("C1", "incident declared")
	require.Error(t, err)
	require.Contains(t, err.Error(), "channel_not_found")
}

func TestArchiveChannel(t *testing.T) {
	c := routeServer(t, map[string]string{
		"conversations.archive": `{"ok":true}`,
	})
	require.NoError(t, c.ArchiveChannel("C1"))
}

func TestArchiveChannelError(t *testing.T) {
	c := routeServer(t, map[string]string{
		"conversations.archive": `{"ok":false,"error":"channel_not_found"}`,
	})
	require.Error(t, c.ArchiveChannel("C1"))
}

func TestInviteUsers(t *testing.T) {
	c := routeServer(t, map[string]string{
		"conversations.invite": `{"ok":true,"channel":{"id":"C1","name":"inc-1"}}`,
	})
	require.NoError(t, c.InviteUsers("C1", []string{"U1", "U2"}))
}

func TestInviteUsersError(t *testing.T) {
	c := routeServer(t, map[string]string{
		"conversations.invite": `{"ok":false,"error":"user_not_found"}`,
	})
	require.Error(t, c.InviteUsers("C1", []string{"U1"}))
}
