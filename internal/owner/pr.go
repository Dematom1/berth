package owner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// prOptIn names the settings that register the pr owner kind.
const prOptIn = `set DIBS_PR_OWNER=1 or "pr_owner": true in the user config`

const prTimeout = 10 * time.Second

var (
	prHost = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
	prName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// pullRequest is a parsed pr owner selector; the stored reference ID is its canonical form.
type pullRequest struct {
	host, owner, repo string
	number            int
}

func (p pullRequest) id() string {
	return p.host + "/" + p.owner + "/" + p.repo + "#" + strconv.Itoa(p.number)
}

// parsePR accepts OWNER/REPO#N (github.com), HOST/OWNER/REPO#N, and https://HOST/OWNER/REPO/pull/N.
func parsePR(selector string) (pullRequest, error) {
	invalid := fmt.Errorf("pr owner must be pr:OWNER/REPO#N, pr:HOST/OWNER/REPO#N, or pr:https://HOST/OWNER/REPO/pull/N; got %q", selector)
	var segments []string
	var number string
	if strings.HasPrefix(selector, "https://") {
		u, err := url.Parse(selector)
		if err != nil || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
			return pullRequest{}, invalid
		}
		parts := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
		if len(parts) != 4 || parts[2] != "pull" {
			return pullRequest{}, invalid
		}
		segments, number = []string{u.Hostname(), parts[0], parts[1]}, parts[3]
	} else {
		path, n, ok := strings.Cut(selector, "#")
		if !ok {
			return pullRequest{}, invalid
		}
		segments, number = strings.Split(path, "/"), n
		if len(segments) == 2 {
			segments = append([]string{"github.com"}, segments...)
		}
	}
	n, err := strconv.Atoi(number)
	if len(segments) != 3 || err != nil || n <= 0 || strings.HasPrefix(number, "+") {
		return pullRequest{}, invalid
	}
	p := pullRequest{strings.ToLower(segments[0]), strings.ToLower(segments[1]), strings.ToLower(segments[2]), n}
	if !prHost.MatchString(p.host) {
		return pullRequest{}, invalid
	}
	for _, name := range []string{p.owner, p.repo} {
		if !prName.MatchString(name) || name == "." || name == ".." || strings.HasPrefix(name, "-") {
			return pullRequest{}, invalid
		}
	}
	return p, nil
}

// observePR reads the PR once through gh api. Any failure, including a 404 or a rate limit, is unknown.
func observePR(ctx context.Context, p pullRequest) Observation {
	ctx, cancel := context.WithTimeout(ctx, prTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "api", "--hostname", p.host, "--method", "GET",
		"repos/"+p.owner+"/"+p.repo+"/pulls/"+strconv.Itoa(p.number))
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return Observation{Liveness: Unknown}
	}
	var pr struct {
		State  string `json:"state"`
		NodeID string `json:"node_id"`
		Number int    `json:"number"`
		Base   struct {
			Repo struct {
				NodeID string `json:"node_id"`
			} `json:"repo"`
		} `json:"base"`
	}
	if json.Unmarshal(out, &pr) != nil || pr.Number != p.number || pr.NodeID == "" || pr.Base.Repo.NodeID == "" {
		return Observation{Liveness: Unknown}
	}
	observation := Observation{Liveness: Unknown, Identity: pr.NodeID + " " + pr.Base.Repo.NodeID}
	switch pr.State {
	case "open":
		observation.Liveness = Alive
	case "closed":
		observation.Liveness = Dead
	}
	return observation
}

// PR follows a GitHub pull request read through the user's gh authentication: open is alive, closed or merged is dead.
func PR() Definition {
	return Definition{Name: "pr",
		Bind: func(ctx context.Context, value string, _ time.Duration, _ time.Time) (Reference, error) {
			p, err := parsePR(strings.TrimPrefix(value, "pr:"))
			if err != nil {
				return Reference{}, err
			}
			observation := observePR(ctx, p)
			switch observation.Liveness {
			case Alive:
				return Reference{Kind: "pr", ID: p.id(), Identity: observation.Identity}, nil
			case Dead:
				return Reference{}, fmt.Errorf("pr owner %s is closed or merged; only an open PR can own a lease", p.id())
			}
			return Reference{}, fmt.Errorf("cannot read pr owner %s through gh api; check gh auth status --hostname %s and that the PR exists", p.id(), p.host)
		},
		Check: func(ctx context.Context, r Reference, _ time.Time) Observation {
			p, err := parsePR(r.ID)
			if err != nil || r.Identity == "" || p.id() != r.ID {
				return Observation{Liveness: Unknown}
			}
			observation := observePR(ctx, p)
			// A different PR or repository at the same path cannot prove the pinned PR closed.
			if observation.Identity != r.Identity {
				observation.Liveness = Unknown
			}
			return observation
		},
	}
}
