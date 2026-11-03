package server

import (
	"errors"
	"fmt"
	"sort"
	"unicode/utf16"

	"github.com/brent/echo/internal/domain"
)

// Byte boundaries indexed by UTF-16 offsets. The middle of a surrogate pair
// deliberately has no boundary, so malformed ranges cannot split a character.
func referenceBoundaries(text string) map[int]int {
	out := map[int]int{0: 0}
	units := 0
	for at, char := range text {
		out[units] = at
		units += utf16.RuneLen(char)
	}
	out[units] = len(text)
	return out
}

func referenceContent(req SendRequest) (string, error) {
	boundaries := referenceBoundaries(req.Content)
	refs := []domain.Segment{}
	for _, ref := range req.Segments {
		if ref.Type == "file" && ref.Range != nil {
			refs = append(refs, ref)
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Range.Start < refs[j].Range.Start })
	masked := []byte(req.Content)
	last := 0
	for _, ref := range refs {
		start, startOK := boundaries[ref.Range.Start]
		end, endOK := boundaries[ref.Range.End]
		if !startOK || !endOK || ref.Range.Start < last || end <= start || ref.Text == "" || req.Content[start:end] != "#"+ref.Text {
			return "", errors.New("file reference no longer matches its message location")
		}
		last = ref.Range.End
		// Filenames containing @ are data, never extra bot recipients.
		for n := start; n < end; n++ {
			masked[n] = ' '
		}
	}
	return string(masked), nil
}

func (s *Server) validateFileSegments(req *SendRequest, bots []domain.Bot, strict bool) error {
	if _, err := referenceContent(*req); err != nil {
		return err
	}
	for i := range req.Segments {
		ref := &req.Segments[i]
		if ref.Type != "file" {
			continue
		}
		ref.AllowedBots = []string{}
		ref.Kind = ""
		if ref.NodeID != "" && ref.NodeID != s.Swarm.Node().ID {
			if strict {
				return errors.New("file reference belongs to another machine")
			}
			continue
		}
		for _, bot := range bots {
			kind, err := s.Files.ReferenceKind(bot, ref.Path)
			if err == nil {
				ref.Kind = kind
				ref.AllowedBots = append(ref.AllowedBots, bot.ID)
			}
		}
		if strict && len(ref.AllowedBots) == 0 {
			return fmt.Errorf("file reference is unavailable to all recipients: %s", ref.Path)
		}
	}
	return nil
}

func utf16Offset(text string) int { return len(utf16.Encode([]rune(text))) }
