package device

import (
	"github.com/google/uuid"
)

// In-memory registry of adoption / forget jobs the dashboard polls.

func (s *Service) startJob(id, kind, ip string) *Job {
	now := s.now()
	job := &Job{ID: uuid.New(), DeviceID: id, Kind: kind, IP: ip, Phase: "starting", StartedAt: now, UpdatedAt: now}
	s.mu.Lock()
	for k, j := range s.jobs {
		if now.Sub(j.UpdatedAt) > jobTTL {
			delete(s.jobs, k)
		}
	}
	s.jobs[job.ID] = job
	s.mu.Unlock()
	return job
}

func (s *Service) updateJob(job *Job, fn func(*Job)) {
	s.mu.Lock()
	fn(job)
	job.UpdatedAt = s.now()
	s.mu.Unlock()
}

func (s *Service) Job(id uuid.UUID) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return Job{}, ErrJobNotFound
	}
	return *job, nil
}
