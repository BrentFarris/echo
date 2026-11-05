package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/brent/echo/internal/domain"
)

func mentionMatches(content string) [][]int {
	masked := codeBlocks.ReplaceAllStringFunc(content, func(v string) string { return strings.Repeat(" ", len(v)) })
	return mentions.FindAllStringSubmatchIndex(masked, -1)
}

func hasGroupSegment(segments []domain.Segment) bool {
	return slices.ContainsFunc(segments, func(seg domain.Segment) bool { return seg.Type == "group" })
}

func (s *Server) groupCapability(node string) bool { return s.peerSupports(node, "bot-groups/1") }
func (s *Server) oneGroupCapable(node string) bool { return s.peerSupports(node, "bot-groups-one/1") }
func (s *Server) peerSupports(node, capability string) bool {
	if node == s.Swarm.Node().ID {
		return true
	}
	for _, peer := range s.Swarm.Directory() {
		if peer.Node.ID == node {
			return slices.Contains(peer.Node.Capabilities, capability)
		}
	}
	return false
}

func (s *Server) checkGroupCapability(node string) error {
	if !s.groupCapability(node) {
		return fmt.Errorf("update Echo on %s to use bot groups", s.Swarm.Namespace(node))
	}
	return nil
}

// Read definitions once per mentioned owner. Membership is bound at the
// entry machine before forwarding. Workers still validate every real bot.
func (s *Server) prepareGroupMentions(ctx context.Context, req *SendRequest) error {
	if req.groupMentions != nil || req.rerunMessageID != "" || req.scheduledBot != "" {
		return nil
	}
	req.groupMentions = map[int]domain.Segment{}
	req.botMentions = map[int]domain.Segment{}
	if !strings.Contains(req.Content, "@") && len(req.Segments) == 0 {
		return nil
	}
	content, err := referenceContent(*req)
	if err != nil {
		return err
	}
	self, origin := s.Swarm.Node().ID, req.OriginNodeID
	if origin == "" {
		origin = self
	}
	boundaries := referenceBoundaries(req.Content)
	chips := map[int]domain.Segment{}
	for _, seg := range req.Segments {
		if seg.Type != "group" && seg.Type != "bot" {
			continue
		}
		if seg.Range == nil {
			if seg.Type == "group" {
				return errors.New("group mention requires a message location")
			}
			continue
		}
		start, ok := boundaries[seg.Range.Start]
		end, endOK := boundaries[seg.Range.End]
		if !ok || !endOK || end <= start || content[start:end] != seg.Text {
			return errors.New("mention no longer matches its message location")
		}
		if _, duplicate := chips[start]; duplicate {
			return errors.New("overlapping bot or group mentions")
		}
		chips[start] = seg
	}
	namespaces := map[string]string{s.Swarm.Namespace(self): self}
	for _, peer := range s.Swarm.Directory() {
		if peer.Authorized && !peer.Blocked {
			namespaces[s.Swarm.Namespace(peer.Node.ID)] = peer.Node.ID
		}
	}
	loaded := map[string][]domain.BotGroup{}
	for _, match := range mentionMatches(content) {
		start, end := match[2]-1, match[3]
		chip := chips[start]
		delete(chips, start)
		if chip.Type == "bot" {
			if chip.Text != content[start:end] {
				return errors.New("bot mention no longer matches its message location")
			}
			req.botMentions[start] = chip
			continue
		}
		name, owner := strings.ToLower(content[match[2]:end]), origin
		if prefix, handle, qualified := strings.Cut(name, "."); qualified {
			owner, name = namespaces[prefix], handle
		}
		if chip.Type == "group" {
			if chip.Text != content[start:end] || chip.GroupID == "" {
				return errors.New("group mention no longer matches its message location")
			}
			if chip.NodeID != "" {
				owner = chip.NodeID
			}
			if req.forwardedGroups && (len(chip.Members) > 0 || chip.DeliveryMode == "one" && len(chip.Candidates) > 0) {
				if chip.DeliveryMode == "one" {
					chip.Members = nil
				}
				req.groupMentions[start] = chip
				continue
			}
		}
		if owner == "" || !s.groupCapability(owner) {
			if chip.Type == "group" {
				if owner == "" || !s.swarmCacheAuthorized(owner) {
					return fmt.Errorf("the machine that owns group %s is unavailable", chip.Text)
				}
				return s.checkGroupCapability(owner)
			}
			continue // Ordinary bot on an older peer, or an unknown namespace.
		}
		groups, ok := loaded[owner]
		if !ok {
			if owner == self {
				groups, err = s.botGroups()
			} else {
				if !s.swarmAvailable(owner) {
					return s.offlineMachine(owner, strings.TrimPrefix(content[start:end], "@"))
				}
				child, cancel := context.WithTimeout(ctx, 3*time.Second)
				err = s.Swarm.JSON(child, owner, "GET", "/api/v1/bot-groups", nil, &groups)
				cancel()
			}
			if err != nil {
				return err
			}
			loaded[owner] = groups
		}
		for _, group := range groups {
			if chip.GroupID != "" && group.ID == chip.GroupID || chip.GroupID == "" && group.Handle == name {
				req.groupMentions[start] = domain.Segment{Type: "group", NodeID: owner, GroupID: group.ID, Text: content[start:end], Members: slices.Clone(group.Members), Range: &domain.SegmentRange{Start: utf16Offset(req.Content[:start]), End: utf16Offset(req.Content[:end])}}
				if group.DeliveryMode == "one" {
					seg := req.groupMentions[start]
					seg.DeliveryMode, seg.Candidates, seg.Members = "one", seg.Members, nil
					req.groupMentions[start] = seg
				}
				break
			}
		}
		if chip.Type == "group" && req.groupMentions[start].GroupID == "" {
			return fmt.Errorf("group %s no longer exists", chip.Text)
		}
	}
	for _, unused := range chips {
		return fmt.Errorf("%s mention is outside addressable message text", unused.Type)
	}
	return nil
}

// Resolve stable member IDs against the same current roster as direct mentions.
// The displayed and model-facing handles are captured at acceptance.
func (s *Server) bindGroup(group domain.Segment, selectBot func(domain.BotGroupMember) (string, bool)) error {
	if len(group.Members) == 0 && group.DeliveryMode != "one" {
		return fmt.Errorf("group %s has no members", group.Text)
	}
	for i, member := range group.Members {
		handle, ok := selectBot(member)
		if !ok {
			return fmt.Errorf("%s member @%s is unavailable on %s", group.Text, member.Handle, s.Swarm.Namespace(member.NodeID))
		}
		group.Members[i].Handle = handle
	}
	return nil
}

func groupModelContent(m domain.Message) string {
	if !hasGroupSegment(m.Segments) {
		return m.Content
	}
	boundaries := referenceBoundaries(m.Content)
	var out strings.Builder
	last := 0
	for _, seg := range m.Segments {
		if seg.Type != "group" || seg.Range == nil {
			continue
		}
		start, ok := boundaries[seg.Range.Start]
		end, endOK := boundaries[seg.Range.End]
		if !ok || !endOK || start < last || end <= start || m.Content[start:end] != seg.Text {
			continue
		}
		out.WriteString(m.Content[last:start])
		for i, member := range seg.Members {
			if i > 0 {
				out.WriteByte(' ')
			}
			out.WriteByte('@')
			out.WriteString(member.Handle)
		}
		last = end
	}
	out.WriteString(m.Content[last:])
	return out.String()
}
