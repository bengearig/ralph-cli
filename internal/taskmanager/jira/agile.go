package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	agileBoardPrefix = "/rest/agile/1.0/board/"
	agileSprintPath  = "/rest/agile/1.0/sprint"
	agileIssuePath   = "/rest/agile/1.0/issue"
	sprintPageSize   = 50
)

// Sprint is a Jira Software sprint. StartDate is zero when Jira reported none
// or it could not be parsed.
type Sprint struct {
	ID        int64
	Name      string
	State     string
	StartDate time.Time
}

type sprintJSON struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	State     string `json:"state"`
	StartDate string `json:"startDate"`
}

func (s sprintJSON) sprint() Sprint {
	return Sprint{ID: s.ID, Name: s.Name, State: s.State, StartDate: parseSprintTime(s.StartDate)}
}

func parseSprintTime(v string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, jiraTimeLayout} {
		if t, err := time.Parse(layout, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

// BoardSprints lists the sprints of a board in the given states, a
// comma-separated list of future, active, and closed.
func (c *Client) BoardSprints(ctx context.Context, board int, states string) ([]Sprint, error) {
	base := agileBoardPrefix + strconv.Itoa(board) + "/sprint"
	var sprints []Sprint
	for page, startAt := 0, 0; ; page++ {
		if page >= maxSearchPages {
			return nil, fmt.Errorf("jira: GET %s: more than %d result pages", base, maxSearchPages)
		}
		q := url.Values{}
		q.Set("state", states)
		q.Set("startAt", strconv.Itoa(startAt))
		q.Set("maxResults", strconv.Itoa(sprintPageSize))
		data, err := c.do(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var resp struct {
			Values []sprintJSON `json:"values"`
			IsLast bool         `json:"isLast"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, fmt.Errorf("jira: GET %s: response is not valid JSON: %w", base, err)
		}
		for _, v := range resp.Values {
			sprints = append(sprints, v.sprint())
		}
		if resp.IsLast || len(resp.Values) == 0 {
			return sprints, nil
		}
		startAt += len(resp.Values)
	}
}

// CardSprint returns the open sprint a card is in, or nil when it is in none.
func (c *Client) CardSprint(ctx context.Context, key string) (*Sprint, error) {
	path := agileIssuePath + "/" + url.PathEscape(key)
	data, err := c.do(ctx, http.MethodGet, path+"?fields=sprint", nil)
	if err != nil {
		return nil, err
	}
	var issue struct {
		Fields struct {
			Sprint *sprintJSON `json:"sprint"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(data, &issue); err != nil {
		return nil, fmt.Errorf("jira: GET %s: response is not valid JSON: %w", path, err)
	}
	if issue.Fields.Sprint == nil {
		return nil, nil
	}
	s := issue.Fields.Sprint.sprint()
	return &s, nil
}

// AddToSprint moves a card into an open or active sprint.
func (c *Client) AddToSprint(ctx context.Context, sprintID int64, key string) error {
	body, err := json.Marshal(struct {
		Issues []string `json:"issues"`
	}{[]string{key}})
	if err != nil {
		return fmt.Errorf("jira: encode sprint request: %w", err)
	}
	_, err = c.do(ctx, http.MethodPost, agileSprintPath+"/"+strconv.FormatInt(sprintID, 10)+"/issue", body)
	return err
}

func isSprintAddPath(path string) bool {
	return strings.HasPrefix(path, agileSprintPath+"/") && strings.HasSuffix(path, "/issue")
}
