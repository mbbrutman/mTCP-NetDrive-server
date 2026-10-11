/*

   mTCP NetDrive Server (mlog.go)
   Copyright (C) 2023-2026 Michael B. Brutman (mbbrutman@gmail.com)
   mTCP web page: http://www.brutman.com/mTCP/mTCP.html


   This file is part of mTCP.

   mTCP is free software: you can redistribute it and/or modify
   it under the terms of the GNU General Public License as published by
   the Free Software Foundation, either version 3 of the License, or
   (at your option) any later version.

   mTCP is distributed in the hope that it will be useful,
   but WITHOUT ANY WARRANTY; without even the implied warranty of
   MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
   GNU General Public License for more details.

   You should have received a copy of the GNU General Public License
   along with mTCP.  If not, see <http://www.gnu.org/licenses/>.


   Description: Our own version of logging with two key enhancements:

   * severity levels
   * message based using channels

*/

// Package mlog provides logging with four different severity levels.  The
// code uses channels to pass log messages around so there is buffering
// and no need for a mutex.
package mlog

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type mLogger struct {
	output        *os.File
	level         int
	writerChannel chan *[]byte
	writerExited  chan bool
}

const (
	DEBUG   = iota // Write log messages marked DEBUG or higher
	INFO           // Write log messages marked INFO or higher
	WARNING        // Write log messages marked WARNING or higher
	ERROR          // Write log messages marked ERROR or higher
	ALWAYS         // Always write these messages regardless of the logging level
)

var levelStrings = []string{
	"DEBUG ",
	"INFO  ",
	"WARN  ",
	"ERROR ",
	"      ",
}

// The default logger.  The exported functions use this, allowing it to be
// shared outside of this module without having to pass it around as a
// parameter.
var defaultLogger *mLogger

// Package initializer
func init() {
	defaultLogger = new(mLogger)
	defaultLogger.output = nil
	defaultLogger.writerChannel = make(chan *[]byte, 100)
	defaultLogger.writerExited = make(chan bool)
}

// I wanted to be able to set the log level either by using one of the constants
// above or using a text string.  Go doesn't allow function overloading, so take
// a parameter of "any" and then do something based on the type.

// SetLogLevel lets you set the default logging level.  Use either a text
// string or the loglevel constants from above.
func SetLogLevel(any interface{}) error {

	switch any.(type) {

	case string:

		l := any.(string)

		switch strings.ToUpper(l) {
		case "DEBUG":
			SetLogLevel(DEBUG)
		case "INFO":
			SetLogLevel(INFO)
		case "WARN":
			SetLogLevel(WARNING)
		case "ERROR":
			SetLogLevel(ERROR)
		default:
			return errors.New("Bad log level specified")
		}

	case int:
		l := any.(int)
		if l < DEBUG || l > ERROR {
			return errors.New("Bad log level specifed")
		} else {
			defaultLogger.level = l
		}

	default:
		return errors.New("mlog.SetLogLevel takes an integer or a string as input.")
	}

	return nil
}

// SetLogOutput opens the specified logfile for writing.  If this is not
// called then log messages do not get written to a file.
func SetLogOutput(logfilename string) error {

	lf, err := os.OpenFile(logfilename, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		return errors.New("Unable to open log file for writing:" + err.Error())
	}

	defaultLogger.output = lf
	go writer()
	return nil
}

// writer - The log file writer.  Spin on a channel and write whatever
// arrives until the channel closes.  Then send a message on another channel to
// indicate that everything is done.
func writer() {

	for bytes := range defaultLogger.writerChannel {
		defaultLogger.output.Write(*bytes)
	}

	defaultLogger.output.Close()
	defaultLogger.writerExited <- true
}

// printf - The actual print routine.  If there is not log file open this silently does nothing.
func printf(level int, format string, v ...any) {

	if defaultLogger.output == nil {
		return
	}

	if level >= defaultLogger.level {
		o := []byte(fmt.Sprintf("%s %s ", time.Now().Format("2006-01-02 15:04:05.0000"), levelStrings[level]))
		o = fmt.Appendf(o, format, v...)
		defaultLogger.writerChannel <- &o
	}

}

// Debug writes a log message if the logging level is set to DEBUG or higher
func Debug(format string, v ...any) { printf(DEBUG, format, v...) }

// Info writes a log message if the logging level is set to INFO or higher
func Info(format string, v ...any) { printf(INFO, format, v...) }

// Warn writes a log message if the logging level is set to WARN or higher
func Warn(format string, v ...any) { printf(WARNING, format, v...) }

// Error writes a log message if the logging level is set to ERROR or higher
func Error(format string, v ...any) { printf(ERROR, format, v...) }

// Always writes a log message regardless of the logging level.  (This is
// useful for things like program startup messages.)
func Always(format string, v ...any) { printf(ALWAYS, format, v...) }

// Shutdown waits for any queued messages to be written, then closes the
// log file.  It returns when that is done.
func Shutdown() {
	if defaultLogger.output == nil {
		return
	}

	Always("Logging shutting down.\n")

	close(defaultLogger.writerChannel)

	// Wait for it to acknowledge that it is all done.
	_ = <-defaultLogger.writerExited
}
