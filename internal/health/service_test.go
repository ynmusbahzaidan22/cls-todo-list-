package health

import (
	"context"
	"errors"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestDatabaseReady(t *testing.T) {
	tests := []struct {
		name string
		ping Pinger
		want bool
	}{
		{name: "connected database", ping: fakePinger{}, want: true},
		{name: "database unavailable", ping: fakePinger{err: errors.New("offline")}, want: false},
		{name: "missing database", ping: nil, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := NewService(test.ping).DatabaseReady(context.Background()); got != test.want {
				t.Fatalf("DatabaseReady() = %t, want %t", got, test.want)
			}
		})
	}
}
