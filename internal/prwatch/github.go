package prwatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type JSONReader func(context.Context, ...string) (any, error)

type ReadError struct{ Err error }

func (e *ReadError) Error() string { return e.Err.Error() }
func (e *ReadError) Unwrap() error { return e.Err }

const commentFields = "id body url path line originalLine diffSide author { login }"
const pageInfoFields = "pageInfo { hasNextPage endCursor }"

const threadQuery = `
query($owner:String!, $repo:String!, $number:Int!, $cursor:String) {
  repository(owner:$owner, name:$repo) {
    pullRequest(number:$number) {
      reviewThreads(first:100, after:$cursor) {
        nodes { id isResolved isOutdated comments(first:100) {
          nodes { ` + commentFields + ` } ` + pageInfoFields + `
        } } ` + pageInfoFields + `
      }
    }
  }
}
`

const repliesQuery = `
query($threadId:ID!, $cursor:String) {
  node(id:$threadId) { ... on PullRequestReviewThread {
    comments(first:100, after:$cursor) { nodes { ` + commentFields + ` } ` + pageInfoFields + ` }
  } }
}
`

func ReadGH(ctx context.Context, args ...string) (any, error) {
	callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(callCtx, "gh", args...)
	var out, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var missing *exec.Error
		if errors.As(err, &missing) {
			return nil, err
		}
		text := []rune(strings.TrimSpace(diagnostic.String()))
		if len(text) > 1200 {
			text = text[:1200]
		}
		if len(text) == 0 {
			text = []rune(err.Error())
		}
		return nil, &ReadError{Err: fmt.Errorf("GitHub read failed: %s", string(text))}
	}
	var value any
	decoder := json.NewDecoder(&out)
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, &ReadError{Err: err}
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, &ReadError{Err: errors.New("trailing GitHub JSON")}
	}
	if object, ok := value.(map[string]any); ok && object["errors"] != nil {
		return nil, &ReadError{Err: errors.New("GitHub GraphQL returned errors")}
	}
	return value, nil
}

type collector struct {
	ctx       context.Context
	pr        PR
	read      JSONReader
	cancelled func() bool
}

func Collect(ctx context.Context, pr PR, read JSONReader, cancelled func() bool) (Snapshot, error) {
	if read == nil {
		return nil, errors.New("GitHub reader is required")
	}
	c := collector{ctx: ctx, pr: pr, read: read, cancelled: cancelled}
	value, err := c.request("pr", "view", pr.URL, "--json", "url,number,state,isDraft,mergeable,mergeStateStatus,headRefOid")
	if err != nil {
		return nil, err
	}
	meta, err := responseObject(value, "PR metadata")
	if err != nil {
		return nil, err
	}
	head, err := requiredString(meta, "headRefOid")
	if err != nil {
		return nil, err
	}
	state, err := requiredString(meta, "state")
	if err != nil {
		return nil, err
	}
	draft, err := requiredBool(meta, "isDraft")
	if err != nil {
		return nil, err
	}
	mergeable, err := requiredString(meta, "mergeable")
	if err != nil {
		return nil, err
	}
	mergeState, err := requiredString(meta, "mergeStateStatus")
	if err != nil {
		return nil, err
	}
	snapshot := Snapshot{
		"head_sha": head, "state": state, "draft": draft,
		"mergeable": mergeable, "merge_state": mergeState,
		"checks": []any{}, "comments": map[string]any{}, "reviews": map[string]any{}, "threads": map[string]any{},
	}
	if state == "MERGED" || state == "CLOSED" {
		return snapshot, nil
	}

	commentRows, err := c.rest(fmt.Sprintf("repos/%s/%s/issues/%s/comments", pr.Owner, pr.Repo, pr.Number), "")
	if err != nil {
		return nil, err
	}
	comments, err := normalizeRows(commentRows)
	if err != nil {
		return nil, err
	}
	reviewRows, err := c.rest(fmt.Sprintf("repos/%s/%s/pulls/%s/reviews", pr.Owner, pr.Repo, pr.Number), "")
	if err != nil {
		return nil, err
	}
	reviews, err := normalizeRows(reviewRows)
	if err != nil {
		return nil, err
	}
	threads, err := c.collectThreads()
	if err != nil {
		return nil, err
	}
	commit := fmt.Sprintf("repos/%s/%s/commits/%s", pr.Owner, pr.Repo, head)
	checkRows, err := c.rest(commit+"/check-runs?filter=latest", "check_runs")
	if err != nil {
		return nil, err
	}
	statusRows, err := c.rest(commit+"/status", "statuses")
	if err != nil {
		return nil, err
	}
	checks, err := normalizeChecks(checkRows, statusRows)
	if err != nil {
		return nil, err
	}
	snapshot["checks"], snapshot["comments"], snapshot["reviews"], snapshot["threads"] = checks, comments, reviews, threads
	return snapshot, nil
}

func (c collector) request(args ...string) (any, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	if c.cancelled != nil && c.cancelled() {
		return nil, ErrStopped
	}
	value, err := c.read(c.ctx, args...)
	if err != nil {
		if ctxErr := c.ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, ErrStopped) {
			return nil, err
		}
		var missing *exec.Error
		if errors.As(err, &missing) {
			return nil, err
		}
		var readErr *ReadError
		if errors.As(err, &readErr) {
			return nil, err
		}
		return nil, &ReadError{Err: err}
	}
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	if c.cancelled != nil && c.cancelled() {
		return nil, ErrStopped
	}
	return value, nil
}

func (c collector) rest(path, key string) ([]any, error) {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	value, err := c.request("api", "--hostname", c.pr.Host, "--paginate", "--slurp", path+separator+"per_page=100")
	if err != nil {
		return nil, err
	}
	pages, ok := value.([]any)
	if !ok {
		return nil, malformed("GitHub REST pagination must be an array")
	}
	rows := []any{}
	for _, rawPage := range pages {
		page := rawPage
		if key != "" {
			object, ok := rawPage.(map[string]any)
			if !ok {
				return nil, malformed("GitHub REST page must be an object")
			}
			var exists bool
			page, exists = object[key]
			if !exists {
				return nil, malformed("GitHub REST page is missing %s", key)
			}
		}
		items, ok := page.([]any)
		if !ok {
			return nil, malformed("GitHub REST page contents must be an array")
		}
		rows = append(rows, items...)
	}
	return rows, nil
}

func (c collector) graphql(query string, variables ...string) (map[string]any, error) {
	args := []string{"api", "--hostname", c.pr.Host, "graphql", "-f", "query=" + query}
	for index := 0; index < len(variables); index += 2 {
		if variables[index+1] == "" {
			continue
		}
		flag := "-f"
		if variables[index] == "number" {
			flag = "-F"
		}
		args = append(args, flag, variables[index]+"="+variables[index+1])
	}
	value, err := c.request(args...)
	if err != nil {
		return nil, err
	}
	object, err := responseObject(value, "GraphQL response")
	if err != nil {
		return nil, err
	}
	if object["errors"] != nil {
		return nil, malformed("GitHub GraphQL returned errors")
	}
	data, ok := object["data"].(map[string]any)
	if !ok {
		return nil, malformed("GitHub GraphQL response is missing data")
	}
	return data, nil
}

func (c collector) collectThreads() (map[string]any, error) {
	threads := map[string]any{}
	cursor := ""
	for {
		data, err := c.graphql(threadQuery, "owner", c.pr.Owner, "repo", c.pr.Repo, "number", c.pr.Number, "cursor", cursor)
		if err != nil {
			return nil, err
		}
		repository, ok := data["repository"].(map[string]any)
		if !ok {
			return nil, malformed("GraphQL response is missing repository")
		}
		pullRequest, ok := repository["pullRequest"].(map[string]any)
		if !ok {
			return nil, malformed("GraphQL response is missing pullRequest")
		}
		connection, ok := pullRequest["reviewThreads"].(map[string]any)
		if !ok {
			return nil, malformed("GraphQL response is missing reviewThreads")
		}
		nodes, next, err := connectionPage(connection)
		if err != nil {
			return nil, err
		}
		for _, raw := range nodes {
			thread, ok := raw.(map[string]any)
			if !ok {
				return nil, malformed("review thread must be an object")
			}
			id, err := requiredString(thread, "id")
			if err != nil {
				return nil, err
			}
			resolved, err := requiredBool(thread, "isResolved")
			if err != nil {
				return nil, err
			}
			outdated, err := requiredBool(thread, "isOutdated")
			if err != nil {
				return nil, err
			}
			comments, ok := thread["comments"].(map[string]any)
			if !ok {
				return nil, malformed("review thread is missing comments")
			}
			rows, replyCursor, err := connectionPage(comments)
			if err != nil {
				return nil, err
			}
			if resolved {
				continue
			}
			for replyCursor != "" {
				data, err := c.graphql(repliesQuery, "threadId", id, "cursor", replyCursor)
				if err != nil {
					return nil, err
				}
				node, ok := data["node"].(map[string]any)
				if !ok {
					return nil, malformed("GraphQL reply response is missing node")
				}
				comments, ok = node["comments"].(map[string]any)
				if !ok {
					return nil, malformed("GraphQL reply response is missing comments")
				}
				more, nextReply, err := connectionPage(comments)
				if err != nil {
					return nil, err
				}
				rows = append(rows, more...)
				replyCursor = nextReply
			}
			replies := make([]any, 0, len(rows))
			for _, row := range rows {
				comment, err := normalizeComment(row)
				if err != nil {
					return nil, err
				}
				replies = append(replies, comment)
			}
			threads[id] = map[string]any{"outdated": outdated, "comments": replies}
		}
		if next == "" {
			return threads, nil
		}
		cursor = next
	}
}

func connectionPage(connection map[string]any) ([]any, string, error) {
	nodes, ok := connection["nodes"].([]any)
	if !ok {
		return nil, "", malformed("GraphQL connection nodes must be an array")
	}
	pageInfo, ok := connection["pageInfo"].(map[string]any)
	if !ok {
		return nil, "", malformed("GraphQL connection is missing pageInfo")
	}
	hasNext, err := requiredBool(pageInfo, "hasNextPage")
	if err != nil {
		return nil, "", err
	}
	if !hasNext {
		return nodes, "", nil
	}
	end, err := requiredString(pageInfo, "endCursor")
	if err != nil || end == "" {
		if err != nil {
			return nil, "", err
		}
		return nil, "", malformed("GraphQL page with more results is missing endCursor")
	}
	return nodes, end, nil
}

func normalizeRows(rows []any) (map[string]any, error) {
	items := map[string]any{}
	for _, row := range rows {
		comment, err := normalizeComment(row)
		if err != nil {
			return nil, err
		}
		items[comment["id"].(string)] = comment
	}
	return items, nil
}

func normalizeComment(raw any) (map[string]any, error) {
	row, ok := raw.(map[string]any)
	if !ok {
		return nil, malformed("feedback row must be an object")
	}
	var id string
	switch value := row["id"].(type) {
	case string:
		id = value
	case json.Number:
		id = value.String()
	default:
		return nil, malformed("feedback id must be a string or number")
	}
	body := ""
	if value, exists := row["body"]; exists && value != nil {
		var ok bool
		body, ok = value.(string)
		if !ok {
			return nil, malformed("feedback body must be a string")
		}
	}
	url, err := firstOptionalString(row, "html_url", "url")
	if err != nil {
		return nil, err
	}
	author, err := feedbackAuthor(row)
	if err != nil {
		return nil, err
	}
	comment := map[string]any{"id": id, "body": body, "url": url, "author": author}
	for _, field := range []string{"path", "state", "commit_id", "submitted_at", "diffSide"} {
		if value, exists := row[field]; exists {
			if value != nil {
				if _, ok := value.(string); !ok {
					return nil, malformed("feedback %s must be a string", field)
				}
			}
			comment[field] = value
		}
	}
	for _, field := range []string{"line", "originalLine"} {
		if value, exists := row[field]; exists {
			if value != nil {
				if _, ok := value.(json.Number); !ok {
					return nil, malformed("feedback %s must be a number", field)
				}
			}
			comment[field] = value
		}
	}
	return comment, nil
}

func feedbackAuthor(row map[string]any) (any, error) {
	var author any
	for _, field := range []string{"user", "author"} {
		value, exists := row[field]
		if !exists || value == nil {
			continue
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil, malformed("feedback %s must be an object", field)
		}
		login, exists := object["login"]
		if !exists || login == nil {
			continue
		}
		if _, ok := login.(string); !ok {
			return nil, malformed("feedback author login must be a string")
		}
		if author == nil {
			author = login
		}
	}
	return author, nil
}

func normalizeChecks(checkRows, statusRows []any) ([]any, error) {
	checks := make([]any, 0, len(checkRows)+len(statusRows))
	for _, raw := range checkRows {
		row, ok := raw.(map[string]any)
		if !ok {
			return nil, malformed("check run must be an object")
		}
		name, err := requiredString(row, "name")
		if err != nil {
			return nil, err
		}
		status, err := requiredString(row, "status")
		if err != nil {
			return nil, err
		}
		conclusion, err := optionalStringValue(row, "conclusion")
		if err != nil {
			return nil, err
		}
		url, err := optionalStringValue(row, "html_url")
		if err != nil {
			return nil, err
		}
		checks = append(checks, map[string]any{"name": name, "status": strings.ToUpper(status), "conclusion": strings.ToUpper(conclusion), "url": nullableString(url, row["html_url"])})
	}
	for _, raw := range statusRows {
		row, ok := raw.(map[string]any)
		if !ok {
			return nil, malformed("commit status must be an object")
		}
		name, err := requiredString(row, "context")
		if err != nil {
			return nil, err
		}
		state, err := requiredString(row, "state")
		if err != nil {
			return nil, err
		}
		url, err := optionalStringValue(row, "target_url")
		if err != nil {
			return nil, err
		}
		conclusion := strings.ToUpper(state)
		if strings.EqualFold(state, "pending") {
			conclusion = ""
		}
		checks = append(checks, map[string]any{"name": name, "status": strings.ToUpper(state), "conclusion": conclusion, "url": nullableString(url, row["target_url"])})
	}
	sortChecks(checks)
	return checks, nil
}

func nullableString(value string, raw any) any {
	if raw == nil {
		return nil
	}
	return value
}

func sortChecks(checks []any) {
	sort.SliceStable(checks, func(i, j int) bool {
		left, _ := json.Marshal(checks[i])
		right, _ := json.Marshal(checks[j])
		return bytes.Compare(left, right) < 0
	})
}

func responseObject(value any, label string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, malformed("%s must be an object", label)
	}
	return object, nil
}

func requiredString(object map[string]any, field string) (string, error) {
	value, ok := object[field].(string)
	if !ok {
		return "", malformed("GitHub response %s must be a string", field)
	}
	return value, nil
}

func requiredBool(object map[string]any, field string) (bool, error) {
	value, ok := object[field].(bool)
	if !ok {
		return false, malformed("GitHub response %s must be a bool", field)
	}
	return value, nil
}

func optionalStringValue(object map[string]any, field string) (string, error) {
	value, exists := object[field]
	if !exists || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", malformed("GitHub response %s must be a string", field)
	}
	return text, nil
}

func firstOptionalString(object map[string]any, fields ...string) (any, error) {
	var selected any
	for _, field := range fields {
		value, exists := object[field]
		if !exists || value == nil {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, malformed("feedback %s must be a string", field)
		}
		if selected == nil {
			selected = text
		}
	}
	return selected, nil
}

func malformed(format string, args ...any) error {
	return &ReadError{Err: fmt.Errorf(format, args...)}
}
