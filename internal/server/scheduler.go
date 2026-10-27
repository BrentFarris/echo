package server

import (
	"context"
	"slices"
	"time"
)

// All scheduler state is protected by Server.mu. Enqueue every recipient in one
// critical section, so shared bots always observe the same dispatch order.
func (s *Server) enqueueLocked(d *dispatch) {
	if s.botQueues == nil {
		s.botQueues = map[string][]*dispatch{}
		s.botReservations = map[string]map[string]bool{}
	}
	for id := range d.admitted {
		s.botQueues[id] = append(s.botQueues[id], d)
	}
	for id := range d.admitted {
		s.admitLocked(id)
	}
}

func (s *Server) admitLocked(id string) {
	bot, err := s.Bot(id)
	if err != nil || s.revoking[id] > 0 {
		return
	}
	queue := s.botQueues[id]
	for len(queue) > 0 {
		d := queue[0]
		if d.ctx.Err() != nil {
			queue[0] = nil
			queue = queue[1:]
			continue
		}
		if !bot.EnableMultitasking && len(s.botReservations[id]) > 0 {
			break
		}
		queue[0] = nil
		queue = queue[1:]
		if s.botReservations[id] == nil {
			s.botReservations[id] = map[string]bool{}
		}
		s.botReservations[id][d.id] = true
		if d.timer == nil {
			d.timer = time.AfterFunc(2*time.Hour, d.cancel)
		}
		close(d.admitted[id])
	}
	if len(queue) == 0 {
		delete(s.botQueues, id)
	} else {
		s.botQueues[id] = queue
	}
}

// Keep reservations until the entire dispatch finishes: a bot's final response
// can still be followed by a handoff from a peer in that same dispatch.
func (s *Server) releaseLocked(d *dispatch) {
	if d.timer != nil {
		d.timer.Stop()
	}
	for id := range d.admitted {
		delete(s.botReservations[id], d.id)
		if len(s.botReservations[id]) == 0 {
			delete(s.botReservations, id)
		}
		queue := s.botQueues[id]
		for i, candidate := range queue {
			if candidate == d {
				queue = slices.Delete(queue, i, i+1)
				break
			}
		}
		if len(queue) == 0 {
			delete(s.botQueues, id)
		} else {
			s.botQueues[id] = queue
		}
		s.admitLocked(id)
	}
}

func (d *dispatch) awaitAdmission(ctx context.Context, botID string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-d.admitted[botID]:
		// Cancellation wins even if admission and cancellation happen together.
		return ctx.Err()
	}
}
