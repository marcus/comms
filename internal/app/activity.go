package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/marcus/comms/internal/domain"
)

// activityRegistry deliberately has no persistence: polls must not queue writes.
// Its observations describe only requests handled by this daemon instance.
type activityRegistry struct {
	mu     sync.Mutex
	agents map[domain.AgentID]domain.AgentActivity
}

func (r *activityRegistry) get(id domain.AgentID) *domain.AgentActivity {
	r.mu.Lock()
	defer r.mu.Unlock()
	value := r.agents[id]
	return &value
}
func (r *activityRegistry) poll(id domain.AgentID, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.agents == nil {
		r.agents = make(map[domain.AgentID]domain.AgentActivity)
	}
	value := r.agents[id]
	value.LastInboxAt = &now
	r.agents[id] = value
}
func (r *activityRegistry) wait(id domain.AgentID, now time.Time) func() {
	r.mu.Lock()
	if r.agents == nil {
		r.agents = make(map[domain.AgentID]domain.AgentActivity)
	}
	value := r.agents[id]
	value.LastWaitAt = &now
	value.OpenWaits++
	r.agents[id] = value
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		value := r.agents[id]
		value.OpenWaits--
		r.agents[id] = value
	}
}

func (s *Service) suggestRecipient(ctx context.Context, ref string, original error) error {
	// An absent author/topic must never be described as an absent recipient.
	if _, err := s.agents.GetAgent(ctx, ref, false, s.clock.Now()); !errors.Is(err, ErrNotFound) {
		return original
	}
	if strings.HasPrefix(ref, "agt_") {
		return original
	}
	target := strings.ToLower(strings.TrimPrefix(ref, "@"))
	if len(target) > 128 {
		return original
	}
	type match struct {
		handle   string
		distance int
	}
	matches := []match{}
	cursor := ""
	for {
		page, err := s.agents.ListAgents(ctx, AgentListRequest{PageRequest: PageRequest{Limit: MaxLimit, Cursor: cursor}}, s.clock.Now())
		if err != nil {
			return original
		}
		for _, agent := range page.Items {
			distance := handleDistance(target, strings.ToLower(agent.Handle))
			if distance <= 2 {
				matches = append(matches, match{agent.Handle, distance})
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].distance != matches[j].distance {
			return matches[i].distance < matches[j].distance
		}
		return matches[i].handle < matches[j].handle
	})
	if len(matches) == 0 {
		return original
	}
	if len(matches) > 3 {
		matches = matches[:3]
	}
	handles := make([]string, len(matches))
	for i, m := range matches {
		handles[i] = "@" + m.handle
	}
	return fmt.Errorf("%w; close handles: %s", original, strings.Join(handles, ", "))
}
func handleDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	row := make([]int, len(br)+1)
	for i := range row {
		row[i] = i
	}
	for i, x := range ar {
		previous := row[0]
		row[0] = i + 1
		for j, y := range br {
			old := row[j+1]
			cost := 0
			if x != y {
				cost = 1
			}
			row[j+1] = min(row[j]+1, row[j+1]+1, previous+cost)
			previous = old
		}
	}
	return row[len(br)]
}
