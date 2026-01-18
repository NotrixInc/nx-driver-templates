package host

import (
	"log"
	"time"
)

type StdLogger struct {
	l *log.Logger
}

func NewStdLogger() *StdLogger {
	return &StdLogger{l: log.Default()}
}

func (s *StdLogger) Debug(msg string, kv ...any) { s.l.Println(append([]any{"DEBUG", msg}, kv...)...) }
func (s *StdLogger) Info(msg string, kv ...any)  { s.l.Println(append([]any{"INFO", msg}, kv...)...) }
func (s *StdLogger) Warn(msg string, kv ...any)  { s.l.Println(append([]any{"WARN", msg}, kv...)...) }
func (s *StdLogger) Error(msg string, kv ...any) { s.l.Println(append([]any{"ERROR", msg}, kv...)...) }

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }
