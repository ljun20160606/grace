package grace

import (
	"fmt"
	golog "log"
)

var log Logger

func init() {
	log = &defaultLogger{}
}

type Logger interface {
	Trace(string, ...interface{})
	Debug(string, ...interface{})
	Info(string, ...interface{})
	Warn(string, ...interface{})
	Error(string, ...interface{})
}

func SetLogger(l Logger) {
	log = l
}

type defaultLogger struct {
}

func (l *defaultLogger) Trace(format string, args ...interface{}) {
	golog.Output(2, fmt.Sprintf(format, args...))
}

func (l *defaultLogger) Debug(format string, args ...interface{}) {
	golog.Output(2, fmt.Sprintf(format, args...))
}

func (l *defaultLogger) Info(format string, args ...interface{}) {
	golog.Output(2, fmt.Sprintf(format, args...))
}

func (l *defaultLogger) Warn(format string, args ...interface{}) {
	golog.Output(2, fmt.Sprintf(format, args...))
}

func (l *defaultLogger) Error(format string, args ...interface{}) {
	golog.Output(2, fmt.Sprintf(format, args...))
}
