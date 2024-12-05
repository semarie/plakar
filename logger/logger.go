package logger

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/log"
)

var infoChannel chan string
var stderrChannel chan string
var debugChannel chan string
var traceChannel chan string

var enableInfo = false
var enableTracing = false

var mutraceSubsystems sync.Mutex
var traceSubsystems map[string]bool

var stdoutLogger *log.Logger
var stderrLogger *log.Logger
var infoLogger *log.Logger
var warnLogger *log.Logger
var debugLogger *log.Logger
var traceLogger *log.Logger

func init() {
	stdoutLogger = log.NewWithOptions(os.Stdout, log.Options{})
	stderrLogger = log.NewWithOptions(os.Stderr, log.Options{})

	infoLogger = log.NewWithOptions(os.Stdout, log.Options{
		Prefix: "info",
	})
	warnLogger = log.NewWithOptions(os.Stderr, log.Options{
		Prefix: "warn",
	})
	debugLogger = log.NewWithOptions(os.Stdout, log.Options{
		Prefix: "debug",
	})
	traceLogger = log.NewWithOptions(os.Stdout, log.Options{
		Prefix: "trace",
	})
}

func Printf(format string, args ...interface{}) {
	infoLogger.Print(fmt.Sprintf(format, args...))
}

func Stdout(format string, args ...interface{}) {
	stdoutLogger.Print(fmt.Sprintf(format, args...))
}

func Stderr(format string, args ...interface{}) {
	stderrLogger.Print(fmt.Sprintf(format, args...))
}

func Info(format string, args ...interface{}) {
	if enableInfo {
		infoLogger.Print(fmt.Sprintf(format, args...))
	}
}

func Warn(format string, args ...interface{}) {
	warnLogger.Print(fmt.Sprintf(format, args...))
}

func Error(format string, args ...interface{}) {
	stderrLogger.Print(fmt.Sprintf(format, args...))
}

func Debug(format string, args ...interface{}) {
	debugLogger.Print(fmt.Sprintf(format, args...))
}

func Trace(subsystem string, format string, args ...interface{}) {
	if enableTracing {
		mutraceSubsystems.Lock()
		_, exists := traceSubsystems[subsystem]
		if !exists {
			_, exists = traceSubsystems["all"]
		}
		mutraceSubsystems.Unlock()
		if exists {
			traceLogger.Print(fmt.Sprintf(subsystem+": "+format, args...))
		}
	}
}

func EnableInfo() {
	enableInfo = true
}
func EnableTrace(traces string) {
	enableTracing = true
	traceSubsystems = make(map[string]bool)
	for _, subsystem := range strings.Split(traces, ",") {
		traceSubsystems[subsystem] = true
	}
}
