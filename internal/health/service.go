package health

import "context"

// Pinger is the minimal database capability required for readiness checks.
type Pinger interface {
	Ping(context.Context) error
}

// Service keeps health decisions independent of HTTP and database implementations.
type Service struct {
	database Pinger
}

func NewService(database Pinger) Service {
	return Service{database: database}
}

func (s Service) DatabaseReady(ctx context.Context) bool {
	return s.database != nil && s.database.Ping(ctx) == nil
}
